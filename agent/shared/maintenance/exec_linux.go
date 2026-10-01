//go:build linux

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
	// linuxTempMinAge is deliberately a full day: /tmp is full of sockets,
	// lockfiles and half-written downloads that a running process is still
	// using, and deleting one at 00:00 breaks a service that was mid-write.
	linuxTempMinAge    = 24 * time.Hour
	linuxVarTmpMinAge  = 7 * 24 * time.Hour
	linuxCacheMinAge   = 7 * 24 * time.Hour
	linuxRotatedLogAge = 30 * 24 * time.Hour
	dropCachesPath     = "/proc/sys/vm/drop_caches"
)

// runStepOS is the whole Linux maintenance surface.
func runStepOS(ctx context.Context, step string) (stepOutcome, error) {
	switch step {
	case TaskCleanupTemp:
		return linuxCleanupTemp(ctx)
	case TaskDiskCheck:
		return linuxDiskCheck(ctx)
	case TaskMemoryHygiene:
		return linuxMemoryHygiene(ctx)
	case TaskLogMaintenance:
		return linuxLogMaintenance(ctx)
	case TaskServiceCleanup:
		return linuxServiceCleanup(ctx)
	}
	return stepOutcome{output: "no linux implementation for step " + step, exitCode: 1},
		fmt.Errorf("unsupported step %q on linux", step)
}

func linuxCleanupTemp(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	// /tmp, /var/tmp, and the user cache. The cache path follows the XDG spec
	// when it is set and falls back to ~/.cache, which is what the desktop
	// tooling on the box already uses.
	roots := []struct {
		path   string
		minAge time.Duration
	}{
		{"/tmp", linuxTempMinAge},
		{"/var/tmp", linuxVarTmpMinAge},
		{userCacheDir(), linuxCacheMinAge},
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

	// Package manager caches. Every one of these is LookPath-guarded because the
	// box may use none, one, or several of them, and an absent package manager
	// is a fact about the distro, not a maintenance failure.
	for _, pm := range [][]string{
		{"apt-get", "clean"},
		{"dnf", "clean", "all"},
		{"yum", "clean", "all"},
		{"pacman", "-Scc", "--noconfirm"},
	} {
		if p, err := exec.LookPath(pm[0]); err == nil {
			out = appendLog(out, run(ctx, p, pm[1:]...))
		}
	}
	// A package cache clean moves bytes out of the local disk, but that happens
	// inside the package manager, not in a file this step deleted. Measuring it
	// would require parsing output we deliberately do not parse; the before/after
	// measure above already accounts for the file-scoped deletions.
	return out, nil
}

func linuxDiskCheck(ctx context.Context) (stepOutcome, error) {
	// fstrim -av is the online TRIM: it tells the drive which blocks are unused
	// and returns. There is deliberately no fsck and no e2fsck here — both need
	// an unmounted filesystem, so running one would either fail or take a
	// partition offline. That is not a disk check, it is an outage.
	p, err := exec.LookPath("fstrim")
	if err != nil {
		// No fstrim is a fact about the kernel or the initramfs, not a failure.
		return stepOutcome{output: "fstrim not present; filesystem TRIM skipped", exitCode: 0}, nil
	}
	return run(ctx, p, "-av"), nil
}

func linuxMemoryHygiene(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	// sync first: drop_caches drops clean page cache, and a dirty page that has
	// not reached the disk yet is a data-loss bug, not a memory win.
	if p, err := exec.LookPath("sync"); err == nil {
		out = appendLog(out, run(ctx, p))
	} else {
		out = appendLog(out, run(ctx, "/bin/sync"))
	}

	// "3" drops the page cache, dentries and inodes. Writing this needs root;
	// an unprivileged agent gets EACCES, which is logged and does not fail the
	// step — sync already ran, so the machine was not left in a worse state.
	// os.WriteFile only applies perm when it has to create the file, and this
	// path always exists, so the 0644 here is documentation, not a chmod.
	if err := os.WriteFile(dropCachesPath, []byte("3\n"), 0o644); err != nil {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("write %s: %v (needs root; page cache kept)", dropCachesPath, err),
			exitCode: 0,
		})
	} else {
		out = appendLog(out, stepOutcome{output: "dropped page cache, dentries and inodes", exitCode: 0})
	}

	// zramctl --reset-all reclaims the compressed swap devices' backing pages.
	if p, err := exec.LookPath("zramctl"); err == nil {
		out = appendLog(out, run(ctx, p, "--reset-all"))
	}
	return out, nil
}

func linuxLogMaintenance(ctx context.Context) (stepOutcome, error) {
	out := stepOutcome{}

	// journalctl --vacuum-size=50M keeps 50M of journal on every filesystem that
	// has one. There is no -f / --force: forcing a rotation during business
	// hours discards the logs an incident investigation needs.
	if p, err := exec.LookPath("journalctl"); err == nil {
		out = appendLog(out, run(ctx, p, "--vacuum-size=50M"))
	}
	// No logrotate -f. Rotated logs are the only ones this step touches.

	// /var/log/*.gz and *.1 — already-rotated logs older than 30 days. The live
	// syslog/messages/auth.log have no rotated suffix and are never matched.
	const varLog = "/var/log"
	before, scanErr := dirSize(varLog, linuxRotatedLogAge)
	if scanErr != nil {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("scan %s: %v", varLog, scanErr),
			exitCode: 0,
		})
	}
	n, err := removeOldFiles(varLog, linuxRotatedLogAge, func(name string) bool {
		return strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".1")
	})
	after, _ := dirSize(varLog, linuxRotatedLogAge)
	out.bytesFreed += freedBytes(before, after)
	out = appendLog(out, stepOutcome{
		output:   fmt.Sprintf("removed %d rotated logs from %s (age > %s)", n, varLog, linuxRotatedLogAge),
		exitCode: 0,
	})
	if err != nil {
		out = appendLog(out, stepOutcome{output: err.Error(), exitCode: 0})
	}
	return out, nil
}

func linuxServiceCleanup(ctx context.Context) (stepOutcome, error) {
	// Read-only. There is no systemctl stop/disable/mask/rm anywhere in this
	// file: this step reports, a human decides.
	out := stepOutcome{}

	units := run(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--no-pager", "--plain")
	out = appendLog(out, units)
	if failed := countFailedUnits(units.output); failed > 0 {
		out = appendLog(out, stepOutcome{
			output:   fmt.Sprintf("%d service units are in the failed state; review manually", failed),
			exitCode: 0,
		})
	}

	timers := run(ctx, "systemctl", "list-timers", "--all", "--no-legend", "--no-pager")
	out = appendLog(out, timers)
	if units.exitCode == 0 && timers.exitCode == 0 {
		out = appendLog(out, stepOutcome{output: "read-only service inventory collected; no unit was modified", exitCode: 0})
	}
	return out, nil
}

// countFailedUnits counts list-units rows whose LOAD/ACTIVE column reads
// "failed". The --plain output is one unit per line, leading dot markers for
// description, so a substring count over " failed " is close enough for a
// reporting step and does not need a full unit-line parser.
func countFailedUnits(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, " failed ") || strings.HasSuffix(strings.TrimSpace(line), " failed") {
			n++
		}
	}
	return n
}

// userCacheDir returns $XDG_CACHE_HOME, or ~/.cache, or "" when neither
// resolves. An unresolvable home is not an error; the cache is simply skipped.
func userCacheDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return x
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache")
}
