//go:build windows

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// winUpdateCache is the Windows Update download cache. Deleting its
	// contents is safe — Windows redownloads what it still needs — but files
	// held open by a running TrustedInstaller or an in-flight update survive,
	// and a survivor means a reboot is likely pending.
	winUpdateCache = `C:\Windows\SoftwareDistribution\Download`
	// winMinidump holds crash dumps older than winMinidumpAge; MEMORY.DMP is
	// handled separately because it has no extension to match on.
	winMinidump    = `C:\Windows\Minidump`
	winMemoryDump  = `C:\Windows\MEMORY.DMP`
	winCBSCab      = `C:\Windows\Logs\CBS`
	winWERArchive  = `C:\ProgramData\Microsoft\Windows\WER\ReportArchive`
	winMinidumpAge = 30 * 24 * time.Hour
	winCBSAge      = 14 * 24 * time.Hour
	winEmptyDirAge = 30 * 24 * time.Hour
	winTempMinAge  = 24 * time.Hour
	winLogMinAge   = 30 * 24 * time.Hour
)

// runStepOS is the whole Windows maintenance surface. Every branch is a fixed
// argv or a Go-native file operation; the two powershell.exe -Command calls
// carry compile-time literal script text and never a payload-derived string.
func runStepOS(ctx context.Context, step string) (stepOutcome, error) {
	switch step {
	case TaskCleanupTemp:
		return windowsCleanupTemp(ctx)
	case TaskDiskCheck:
		return windowsDiskCheck(ctx)
	case TaskMemoryHygiene:
		return windowsMemoryHygiene(ctx)
	case TaskLogMaintenance:
		return windowsLogMaintenance(ctx)
	case TaskServiceCleanup:
		return windowsServiceCleanup(ctx)
	}
	// Unreachable: Run checks Allowed() before calling this. Present so a
	// future step constant added to `allowed` without an OS branch fails
	// loudly instead of silently reporting a clean sweep.
	return stepOutcome{output: "no windows implementation for step " + step, exitCode: 1},
		fmt.Errorf("unsupported step %q on windows", step)
}

func windowsCleanupTemp(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	roots := []string{
		os.TempDir(),
		`C:\Windows\Temp`,
		winUpdateCache,
	}
	// deniedRoots collects the roots the process could not read or write. Every
	// one of them is a case where the sweep removed nothing, and on Windows they
	// are all the same root cause: the agent is not elevated. Tracking the roots
	// themselves rather than counting refusals is what keeps the summary honest
	// — one root refused at both the scan and the delete pass is still one root,
	// and counting refusals printed "5 of 3 cleanup roots" here.
	denied := map[string]bool{}
	for _, root := range roots {
		before, beforeErr := dirSize(root, winTempMinAge)
		if beforeErr != nil {
			// A root that is absent is normal (SoftwareDistribution\Download does
			// not exist until the first update). A root that exists and cannot
			// be walked is a privilege problem, and it is worth failing over:
			// the transcript used to say "scan ...: Access is denied" and still
			// exit 0, which reported a completed sweep that had cleaned nothing.
			if !errors.Is(beforeErr, fs.ErrNotExist) {
				denied[root] = true
				out = appendLog(out, stepOutcome{
					output:   fmt.Sprintf("scan %s: %v", root, beforeErr),
					exitCode: 1,
				})
			}
		}
		n, err := removeOldFiles(root, winTempMinAge, func(string) bool { return true })
		after, _ := dirSize(root, winTempMinAge)
		out.bytesFreed += freedBytes(before, after)
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("removed %d files from %s (age > %s)", n, root, winTempMinAge),
			exitCode: 0,
		})
		if err != nil {
			// Same rule as the scan: a delete that was refused for want of
			// privilege left the files in place, so the step is not clean.
			denied[root] = true
			out = appendLog(out, stepOutcome{
				output:   fmt.Sprintf("delete pass on %s: %v", root, err),
				exitCode: 1,
			})
		}
	}
	deniedRoots := len(denied)

	// Recycle Bin. Clear-RecycleBin is a literal cmdlet invocation; -ErrorAction
	// SilentlyContinue keeps an empty bin from reddening the sweep.
	out = appendLog(out, run(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"Clear-RecycleBin -Force -ErrorAction SilentlyContinue"))

	// Component store cleanup. Dism.exe is absent on Windows Home, so it is
	// LookPath-guarded rather than treated as a failure.
	if p, err := exec.LookPath("Dism.exe"); err == nil {
		out = appendLog(out, run(ctx, p, "/Online", "/Cleanup-Image", "/StartComponentCleanup"))
	} else {
		out = appendLog(out, stepOutcome{output: "Dism.exe not present; component store cleanup skipped", exitCode: 0})
	}

	// A file left in the Windows Update cache was almost certainly locked by
	// an update still in flight, which means the machine has work pending.
	if survivors, err := countFiles(winUpdateCache, winTempMinAge); err == nil && survivors > 0 {
		out.rebootNeeded = true
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("%d update-cache files survived the delete pass; a reboot is likely pending", survivors),
			exitCode: 0,
		})
	}
	// The single explanation the console needs. DISM prints its own "elevated
	// permissions are required" line, but that arrives as a subprocess exit
	// code among several and reads as one more failure in a long transcript.
	// Here it is stated as the step's reason, next to the status it explains.
	//
	// The count is of distinct roots, not of refusals: one root can be refused
	// at the scan and again at the delete pass, and counting both made this
	// print "5 of 3 cleanup roots" on a three-root machine.
	if deniedRoots > 0 {
		out.output = strings.TrimSpace(out.output + "\n" + fmt.Sprintf(
			"all %d cleanup roots were refused; install the agent as a Windows "+
				"Service so it runs elevated", deniedRoots))
	}
	return out, nil
}

func windowsDiskCheck(ctx context.Context) (stepOutcome, error) {
	// Enumerate fixed volumes rather than hardcoding C:. DriveType=3 is
	// DiskDrive: local fixed disk, which excludes removable and network drives
	// that a maintenance sweep has no business touching.
	volumes := run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3').DeviceID")
	out := stepOutcome{output: strings.TrimSpace(volumes.output)}
	if volumes.exitCode != 0 {
		return out, fmt.Errorf("enumerate fixed volumes: exit %d", volumes.exitCode)
	}

	var list []string
	for _, line := range strings.Split(volumes.output, "\n") {
		v := strings.TrimSpace(line)
		// Output is one bare device id per line; anything with a space in it is
		// a formatted header or a localized message, not a volume.
		if len(v) >= 2 && v[1] == ':' && !strings.ContainsAny(v, " \t") {
			list = append(list, v)
		}
	}
	if len(list) == 0 {
		return out, fmt.Errorf("no fixed volumes found")
	}

	for _, vol := range list {
		// chkdsk /scan is the online scan. Plain `chkdsk` on a live volume would
		// offer to schedule itself for the next reboot, which takes the machine
		// offline at a moment nobody chose.
		out = appendLog(out, run(ctx, "chkdsk.exe", vol, "/scan"))
		// defrag /L is the online optimization pass for SSDs and HDD alike.
		out = appendLog(out, run(ctx, "defrag.exe", vol, "/L"))
	}
	// disk_check reports 0 bytes freed by design: chkdsk /scan and defrag /L
	// change no file sizes. A fabricated number here would be worse than none.
	//
	// A non-zero exitCode here is the volume refusing the scan for want of
	// elevation, not a corrupt filesystem. appendLog carries it up so the step
	// reads as failed rather than as a clean volume nobody looked at.
	return out, nil
}

func windowsMemoryHygiene(ctx context.Context) (stepOutcome, error) {
	// EmptyStandbyList is a Sysinternals tool that is not vendored — bundling a
	// third-party binary is a licensing and supply-chain decision, not a code
	// one. Its absence is reported as completed-with-a-reason, not as a failure:
	// reddening every endpoint for a step that correctly did nothing is how an
	// operator learns to ignore the alert.
	if p, err := exec.LookPath("EmptyStandbyList.exe"); err == nil {
		return run(ctx, p), nil
	}
	return stepOutcome{
		output:   "EmptyStandbyList.exe not installed; standby list not trimmed (no-op)",
		exitCode: 0,
	}, nil
}

func windowsLogMaintenance(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	// C:\Windows\Minidump\*.dmp — old crash dumps only.
	before, _ := dirSize(winMinidump, winMinidumpAge)
	n, err := removeOldFiles(winMinidump, winMinidumpAge, func(name string) bool {
		return strings.EqualFold(filepath.Ext(name), ".dmp")
	})
	after, _ := dirSize(winMinidump, winMinidumpAge)
	out.bytesFreed += freedBytes(before, after)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d crash dumps from %s (age > %s)", n, winMinidump, winMinidumpAge),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}

	// MEMORY.DMP is a single file, matched by exact name because it has no
	// extension and lives in C:\Windows alongside thousands of other files.
	if info, err := os.Stat(winMemoryDump); err == nil && time.Since(info.ModTime()) >= winLogMinAge {
		size := info.Size()
		if err := os.Remove(winMemoryDump); err != nil {
			out = appendLog(out, stepOutcome{
				output:   fmt.Sprintf("remove %s: %v", winMemoryDump, err),
				exitCode: 0,
			})
		} else {
			out.bytesFreed += size
			out = appendLog(out, stepOutcome{
				output:   fmt.Sprintf("removed %s (%d bytes)", winMemoryDump, size),
				exitCode: 0,
			})
		}
	}

	// C:\Windows\Logs\CBS\*.cab — the compressed cabinets from a servicing
	// operation. The active CBS.log is a .log and is never matched, which is the
	// whole point: deleting the live log breaks Windows Update diagnostics.
	before, _ = dirSize(winCBSCab, winCBSAge)
	n, err = removeOldFiles(winCBSCab, winCBSAge, func(name string) bool {
		return strings.EqualFold(filepath.Ext(name), ".cab")
	})
	after, _ = dirSize(winCBSCab, winCBSAge)
	out.bytesFreed += freedBytes(before, after)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d CBS archives from %s (age > %s)", n, winCBSCab, winCBSAge),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}

	// Windows Error Reporting archives. Only ReportArchive holds closed-out
	// reports; ReportQueue is live and is not touched.
	before, _ = dirSize(winWERArchive, winLogMinAge)
	dirs, err := removeEmptyDirs(winWERArchive, winLogMinAge)
	after, _ = dirSize(winWERArchive, winLogMinAge)
	out.bytesFreed += freedBytes(before, after)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d empty WER archive directories under %s", dirs, winWERArchive),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}
	return out, nil
}

func windowsServiceCleanup(ctx context.Context) (stepOutcome, error) {
	// Read-only by design. This step reports what looks orphaned; it never
	// stops, disables, disables-on-boot, or deletes a service or a scheduled
	// task. A maintenance sweep that silently disables a vendor agent has
	// broken the machine it was sent to protect.
	out := stepOutcome{}

	services := run(ctx, "sc.exe", "query", "state=", "all")
	out = appendLog(out, services)
	if stopped := countStoppedServices(services.output); stopped > 0 {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("%d services are in a stopped state; review manually", stopped),
			exitCode: 0,
		})
	}

	tasks := run(ctx, "schtasks.exe", "/query", "/fo", "CSV", "/nh")
	out = appendLog(out, tasks)
	if n := countScheduledTasks(tasks.output); n > 0 {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("%d scheduled tasks registered", n),
			exitCode: 0,
		})
	}

	// Empty deployment staging directories. The agent's own installer stages
	// under os.TempDir() as epm_<task>_<file> and leaves the directory behind
	// after a successful install, so that is the primary target. Any other
	// provably-empty directory there is a leftover too.
	//
	// os.Remove succeeds only on an empty directory — that is the safety
	// property, and it is why this is not os.RemoveAll.
	staging := os.TempDir()
	dirs, err := removeEmptyDirs(staging, winEmptyDirAge)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d empty deployment staging directories under %s (age > %s)", dirs, staging, winEmptyDirAge),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}
	// service_cleanup reports 0 bytes freed: it deletes no files, and any file
	// removal here would be a change of scope, not a cleanup.
	return out, nil
}

// countStoppedServices parses the `sc query state= all` transcript and counts
// the services sitting in the STOPPED state. A stopped service is not
// necessarily broken — plenty are stopped on purpose — so this is information
// for an operator, not an action.
func countStoppedServices(output string) int {
	stopped := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "STATE" {
			continue
		}
		if strings.EqualFold(fields[1], "STOPPED") {
			stopped++
		}
	}
	return stopped
}

// countScheduledTasks counts the data rows of a schtasks /fo CSV /nh listing.
// /nh suppresses the header row, so every comma-bearing line is a task record.
func countScheduledTasks(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ",") {
			continue
		}
		n++
	}
	return n
}

// countFiles counts regular files directly under root that are at least minAge
// old, without deleting anything. Used to decide whether a reboot is pending.
func countFiles(root string, minAge time.Duration) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			continue
		}
		n++
	}
	return n, nil
}
