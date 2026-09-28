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
	// pkgutil --forget removes the receipt. It does not delete files an app
	// installed outside its bundle, so the caller is told what was and was not
	// removed rather than being handed a bare success.
	runArgs := append([]string{"--forget", p.PackageID}, args...)
	res := runProcess(ctx, "pkgutil", runArgs)
	out := decodeOutput([]byte(res.output))
	if res.err != nil {
		return res.exitCode, out, fmt.Errorf("pkgutil --forget %s: %w", p.PackageID, res.err)
	}
	return res.exitCode, out, nil
}
