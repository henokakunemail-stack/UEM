//go:build linux

package patch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func scanOS(ctx context.Context) ([]PatchItem, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	if _, err := exec.LookPath("apt-get"); err == nil {
		return scanApt(ctxTimeout)
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return scanDnf(ctxTimeout, "dnf")
	}
	if _, err := exec.LookPath("yum"); err == nil {
		return scanDnf(ctxTimeout, "yum")
	}

	// Not an empty list. A machine with no package manager has not been shown
	// to be up to date, and the console reads an empty list as "Fully
	// Compliant" -- the same green panel a real scan of a patched machine
	// produces. An error here shows as a failed scan instead, which is what
	// this state deserves.
	return nil, fmt.Errorf(
		"no supported package manager found (looked for apt-get, dnf, yum); this host cannot be patch-scanned")
}

func scanApt(ctx context.Context) ([]PatchItem, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "apt-get", "-s", "dist-upgrade")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("apt-get simulation failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	var items []PatchItem
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// apt has printed this prefix both with and without a colon across
		// releases ("Inst pkg ..." and "Inst: pkg ..."). Matching only one of
		// them silently yields zero patches on every other version, which is
		// the failure this whole path exists to prevent.
		var rest string
		switch {
		case strings.HasPrefix(line, "Inst: "):
			rest = line[len("Inst: "):]
		case strings.HasPrefix(line, "Inst "):
			rest = line[len("Inst "):]
		default:
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		items = append(items, PatchItem{
			PatchID:        fields[0],
			Title:          "Update for " + fields[0],
			Description:    line,
			Severity:       SeverityImportant,
			Category:       aptCategory(fields),
			InstalledState: StateMissing,
		})
	}
	return items, nil
}

// aptCategory classifies a simulated upgrade from the origin suite apt prints
// inside the parentheses:
//
//	curl [7.81.0-1] (7.81.0-1ubuntu1.15 Ubuntu:22.04/jammy-security [amd64])
//	                          ^^^^^^^^^^^^^^^^^^^^ the suite
//
// Without this, every package on the host is stamped CategorySecurity, so the
// console's "Critical / Security" figure counts ordinary updates and a genuine
// security update is indistinguishable from a routine one.
func aptCategory(fields []string) string {
	suite := ""
	for i, f := range fields {
		if strings.HasPrefix(f, "(") && i+2 < len(fields) {
			// The token after "(" is the version; the one after it is the suite.
			suite = fields[i+2]
			break
		}
	}
	switch {
	case strings.Contains(suite, "security"):
		return CategorySecurity
	case strings.Contains(suite, "backports"), strings.Contains(suite, "unstable"):
		return CategoryFeature
	default:
		return CategoryUpdates
	}
}

func scanDnf(ctx context.Context, bin string) ([]PatchItem, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "check-update", "-q")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	// check-update exits 100 to mean "updates are available" and 0 to mean
	// "nothing pending". Every other exit is a real failure -- an unreachable
	// mirror, another process holding the lock -- and its stdout lists nothing.
	// Swallowing it reported every broken repository as an up-to-date host.
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 100) {
		return nil, fmt.Errorf("%s check-update failed: %w: %s",
			bin, err, strings.TrimSpace(stderr.String()))
	}

	var items []PatchItem
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	inSecurity := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// "Security:" introduces a section. When it shares a line with the
		// first entry, that entry is parsed here rather than dropped along
		// with the header -- discarding these lines hid exactly the updates
		// this tool exists to report.
		security := inSecurity
		if rest, ok := strings.CutPrefix(line, "Security:"); ok {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				inSecurity = true
				continue
			}
			security, line = true, rest
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		item := PatchItem{
			// Either "pkg.arch version repo" or, under a security section,
			// a single "pkg-version" token. Both are valid install specs for
			// dnf, so the token is passed through as the identifier.
			PatchID:        fields[0],
			Title:          "Update for " + fields[0],
			Severity:       SeverityImportant,
			Category:       CategoryUpdates,
			InstalledState: StateMissing,
		}
		if security {
			item.Category = CategorySecurity
		}
		if len(fields) >= 2 {
			item.Description = "Available version: " + fields[1]
		} else {
			item.Description = line
		}
		items = append(items, item)
	}
	return items, nil
}
