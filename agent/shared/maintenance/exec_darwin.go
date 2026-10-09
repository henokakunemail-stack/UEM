//go:build darwin

package maintenance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	darwinTempMinAge    = 24 * time.Hour
	darwinCacheMinAge   = 7 * 24 * time.Hour
	darwinCrashMinAge   = 30 * 24 * time.Hour
	darwinLogKeepWindow = "7d"
	darwinDataVolume    = "/System/Volumes/Data"
	darwinCrashDirName  = "DiagnosticReports"
)

// runStepOS is the whole macOS maintenance surface.
func runStepOS(ctx context.Context, step string) (stepOutcome, error) {
	switch step {
	case TaskCleanupTemp:
		return darwinCleanupTemp(ctx)
	case TaskDiskCheck:
		return darwinDiskCheck(ctx)
	case TaskMemoryHygiene:
		return darwinMemoryHygiene(ctx)
	case TaskLogMaintenance:
		return darwinLogMaintenance(ctx)
	case TaskServiceCleanup:
		return darwinServiceCleanup(ctx)
	case TaskFlushDNS:
		return darwinFlushDNS(ctx)
	case TaskSecurityAudit:
		return darwinSecurityAudit(ctx)
	case TaskSystemIntegrity:
		return darwinSystemIntegrity(ctx)
	}
	return stepOutcome{output: "no darwin implementation for step " + step, exitCode: 1},
		fmt.Errorf("unsupported step %q on darwin", step)
}

func darwinCleanupTemp(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	roots := []struct {
		path   string
		minAge time.Duration
	}{
		{filepath.Join(userHome(), "Library", "Caches"), darwinCacheMinAge},
	}
	// /private/var/folders/*/*/T is the per-user TMPDIR on modern macOS. The two
	// wildcard levels are the per-user and per-boot hash components; each is
	// read with os.ReadDir rather than globbed, so a symlinked or unreadable
	// entry is skipped rather than followed.
	for _, tdir := range darwinPerUserTempDirs() {
		roots = append(roots, struct {
			path   string
			minAge time.Duration
		}{tdir, darwinTempMinAge})
	}

	for _, r := range roots {
		if r.path == "" {
			continue
		}
		before, scanErr := dirSize(r.path, r.minAge)
		if scanErr != nil {
			out = appendLog(out, stepOutcome{
				output:   fmt.Sprintf("scan %s: %v", r.path, scanErr),
				exitCode: 0,
			})
		}
		n, err := removeOldFiles(r.path, r.minAge, func(string) bool { return true })
		after, _ := dirSize(r.path, r.minAge)
		out.bytesFreed += freedBytes(before, after)
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("removed %d files from %s (age > %s)", n, r.path, r.minAge),
			exitCode: 0,
		})
		if err != nil {
			out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
		}
	}
	return out, nil
}

func darwinDiskCheck(ctx context.Context) (stepOutcome, error) {
	// diskutil verifyVolume is the read-only online check. There is no
	// fsck and no scheduled offline pass: macOS has no user-facing TRIM
	// command, and an unmounted check would be an outage.
	out := stepOutcome{}
	volumes := []string{"/"}
	if _, err := os.Stat(darwinDataVolume); err == nil {
		volumes = append(volumes, darwinDataVolume)
	}
	for _, vol := range volumes {
		// runLong, for the reason chkdsk is: a volume verification reads the
		// whole filesystem, so it scales with what is on the disk. A flat
		// ceiling makes the volume-health check unable to report a healthy
		// large volume, which is the same inversion that made disk_check fail
		// on Windows for a disk that was fine.
		out = appendLog(out, runLong(ctx, "/usr/sbin/diskutil", "verifyVolume", vol))
	}
	return out, nil
}

func darwinMemoryHygiene(ctx context.Context) (stepOutcome, error) {
	// purge is gone from macOS 10.9 and later. Its absence is a fact about the
	// OS release, not a maintenance failure — reporting it as failed would
	// redden every Mac in the fleet for a step that correctly did nothing.
	if p, err := exec.LookPath("purge"); err == nil {
		return run(ctx, p), nil
	}
	return stepOutcome{
		output:   "purge is not present on this macOS release; no memory trim performed (no-op)",
		exitCode: 0,
	}, nil
}

func darwinLogMaintenance(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	// /usr/bin/log erase keeps 7 days of the unified log. A non-zero exit is
	// logged but does not fail the step: on a managed Mac the agent is often
	// not entitled to erase the log, and that is worth recording, not reddening.
	// runLong: the unified log erase is bounded by the size of the log, not by
	// the speed of the machine, and on a long-lived host it is a multi-minute
	// operation. The refusal branch below is for a non-zero exit, not for a
	// timeout — a killed erase leaves the log in place either way, and calling
	// that an entitlement problem would be the same confident wrong answer the
	// elevation message used to be.
	erase := runLong(ctx, "/usr/bin/log", "erase", "--keep", darwinLogKeepWindow)
	out = appendSecondaryLog(out, erase, "unified log erase")
	if erase.exitCode != 0 {
		out = appendLog(out, stepOutcome{
			output:   "unified log erase did not complete (insufficient entitlement?); retained",
			exitCode: 0,
		})
	}

	// ~/Library/Logs/DiagnosticReports/*.crash — user crash reports older than
	// 30 days. /Library/Logs/DiagnosticReports is the system-wide equivalent
	// and is left to the OS.
	crashDir := filepath.Join(userHome(), "Library", "Logs", darwinCrashDirName)
	before, _ := dirSize(crashDir, darwinCrashMinAge)
	n, err := removeOldFiles(crashDir, darwinCrashMinAge, func(name string) bool {
		return strings.HasSuffix(name, ".crash")
	})
	after, _ := dirSize(crashDir, darwinCrashMinAge)
	out.bytesFreed += freedBytes(before, after)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d crash reports from %s (age > %s)", n, crashDir, darwinCrashMinAge),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}
	return out, nil
}

func darwinServiceCleanup(ctx context.Context) (stepOutcome, error) {
	// Read-only. launchctl list is an inventory; there is no bootout, no
	// unload and no remove anywhere in this file.
	out := run(ctx, "/bin/launchctl", "list")
	if orphan := countOrphanedLaunchAgents(out.output); orphan > 0 {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("%d launchd entries have no running process (last exit %s); review manually", orphan, "see listing above"),
			exitCode: 0,
		})
	} else {
		out = appendLog(out, stepOutcome{
			output:   "no launchd entry without a process; nothing to report",
			exitCode: 0,
		})
	}
	// service_cleanup reports 0 bytes freed by design: it reports, it does not delete.
	return out, nil
}

// countOrphanedLaunchAgents counts launchctl list rows with no PID. The output
// is three tab-separated columns: PID, status, label. A dash in the PID column
// means the job is loaded but not currently running, which is normal for
// on-demand agents and an "orphan" only by appearance — hence a count, not a
// verdict.
func countOrphanedLaunchAgents(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 3 {
			continue
		}
		if strings.TrimSpace(fields[0]) == "-" {
			n++
		}
	}
	return n
}

// darwinPerUserTempDirs returns /private/var/folders/<a>/<b>/T for every user
// bucket currently present. Each level is read with os.ReadDir so an
// unreadable entry is skipped instead of aborting the sweep.
func darwinPerUserTempDirs() []string {
	const root = "/private/var/folders"
	var out []string
	users, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		hashes, err := os.ReadDir(filepath.Join(root, u.Name()))
		if err != nil {
			continue
		}
		for _, h := range hashes {
			if !h.IsDir() {
				continue
			}
			t := filepath.Join(root, u.Name(), h.Name(), "T")
			if info, err := os.Stat(t); err == nil && info.IsDir() {
				out = append(out, t)
			}
		}
	}
	return out
}

// userHome returns $HOME, falling back to the Go-resolved home directory. An
// unresolvable home returns "", which every caller treats as "skip".
func userHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func darwinFlushDNS(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}
	out = appendLog(out, run(ctx, "/usr/bin/dscacheutil", "-flushcache"))
	out = appendSecondaryLog(out, run(ctx, "/usr/bin/killall", "-HUP", "mDNSResponder"), "mDNSResponder")
	return out, nil
}

func darwinSecurityAudit(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}
	out = appendLog(out, run(ctx, "/usr/sbin/spctl", "--status"))
	out = appendSecondaryLog(out, run(ctx, "/usr/bin/csrutil", "status"), "csrutil")
	return out, nil
}

func darwinSystemIntegrity(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}
	out = appendLog(out, run(ctx, "/usr/sbin/diskutil", "apfs", "list"))
	return out, nil
}

