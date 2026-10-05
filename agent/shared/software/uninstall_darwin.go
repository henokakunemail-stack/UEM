//go:build darwin

package software

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// findInstalled lists macOS installer receipts.
//
// pkgutil --pkgs is the receipt database, which is the closest macOS has to
// dpkg's status file: it is what /var/db/receipts holds, it is stable, and it
// does not need a subprocess round trip per query. An app installed by copying
// a .app bundle -- which is most Mac software -- has no receipt at all, so
// system_profiler is consulted as a second source rather than pretending the
// receipt database is the whole truth.
func findInstalled(ctx context.Context, name string) ([]*installedProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []*installedProgram
	seen := make(map[string]bool)

	for _, id := range listReceipts(ctx) {
		// A receipt id is a reverse-DNS bundle identifier such as
		// com.rarlab.rar. Match on it as well as on the display name, because an
		// operator who recorded a bundle id means that and nothing else.
		if !nameMatches(name, id) && !nameMatches(name, lastComponent(id)) {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, &installedProgram{
			Name:        lastComponent(id),
			Version:     receiptVersion(ctx, id),
			Publisher:   "pkgutil",
			PackageID:   id,
			Description: "macOS installer receipt " + id,
		})
	}
	return out, nil
}

func listReceipts(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "pkgutil", "--pkgs").Output()
	if err != nil {
		return nil
	}
	var ids []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if id := strings.TrimSpace(sc.Text()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func receiptVersion(ctx context.Context, id string) string {
	out, err := exec.CommandContext(ctx, "pkgutil", "--pkg-info", id).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "version: "); ok {
			return v
		}
	}
	return ""
}

func lastComponent(dotted string) string {
	if i := strings.LastIndex(dotted, "."); i >= 0 && i+1 < len(dotted) {
		return dotted[i+1:]
	}
	return dotted
}

func runPlatformUninstall(ctx context.Context, p *installedProgram, args []string) (int, string, error) {
	if p.PackageID == "" {
		return -1, "", errors.New("no receipt id recorded; cannot remove " + p.Name)
	}
	runArgs := append([]string{"--forget", p.PackageID}, args...)
	res := runProcess(ctx, "pkgutil", runArgs)
	out := decodeOutput([]byte(res.output))
	if res.err != nil {
		return res.exitCode, out, fmt.Errorf("pkgutil --forget %s: %w", p.PackageID, res.err)
	}
	// pkgutil --forget drops the receipt and nothing else. The .app bundle, and
	// anything the installer wrote outside it, are still on disk -- this is the
	// same reason pkgutil's own documentation says the command exists for a
	// package that is already gone, not as an uninstaller.
	//
	// So the exit code says the receipt went, and this sentence says the program
	// did not. Reporting a bare exit 0 here is how an operator ends up believing a
	// compliance violation was remediated while the app is still installed, which
	// is the false success this row exists to prevent. The output is appended
	// rather than substituted so the exit code stays whatever pkgutil returned.
	if out != "" {
		out += "\n"
	}
	out += fmt.Sprintf(
		"[receipt %s was forgotten; its files were NOT deleted. Remove %s manually "+
			"to finish this uninstall -- the receipt database is not an uninstaller]",
		p.PackageID, p.Name)
	return res.exitCode, out, nil
}
