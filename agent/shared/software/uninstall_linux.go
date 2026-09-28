//go:build linux

package software

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// findInstalled reads dpkg's status database.
//
// /var/lib/dpkg/status is parsed directly rather than shelling out to
// `dpkg -l` or `rpm -qa`. dpkg's own inventory format is a stable, documented
// paragraph file, it needs no subprocess, and it cannot be half-broken by a
// package manager mid-transaction the way a live query can. rpm's database is
// binary and has no such file, so an rpm box falls through to the command.
func findInstalled(ctx context.Context, name string) ([]*installedProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := os.Stat("/var/lib/dpkg/status"); err == nil {
		return findDpkgInstalled(name)
	}
	return findRpmInstalled(ctx, name)
}

func findDpkgInstalled(want string) ([]*installedProgram, error) {
	f, err := os.Open("/var/lib/dpkg/status")
	if err != nil {
		return nil, fmt.Errorf("read dpkg status: %w", err)
	}
	defer f.Close()

	var out []*installedProgram
	var name, version, status, desc string
	// A paragraph is separated by a blank line, and the fields we need are
	// single-line except Description, whose continuation lines start with a space.
	flush := func() {
		defer func() { name, version, status, desc = "", "", "", "" }()
		// Only fully installed packages count. "deinstall ok config-files" is
		// a package whose files are gone but whose registration remains, and
		// removing it is not what an operator asking to uninstall means.
		if name == "" || status != "install ok installed" {
			return
		}
		if !nameMatches(want, name) {
			return
		}
		out = append(out, &installedProgram{
			Name:        name,
			Version:     version,
			Publisher:   "dpkg",
			PackageID:   name,
			Description: firstLine(desc),
		})
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of Description
		}
		field, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch field {
		case "Package":
			name = value
		case "Version":
			version = value
		case "Status":
			status = value
		case "Description":
			desc = value
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read dpkg status: %w", err)
	}
	return out, nil
}

// findRpmInstalled shells out to rpm, whose database is binary and has no
// parseable file equivalent to dpkg's.
func findRpmInstalled(ctx context.Context, want string) ([]*installedProgram, error) {
	res := runProcess(ctx, "rpm", []string{"-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\t%{VENDOR}\n"})
	if res.err != nil {
		return nil, fmt.Errorf("query rpm database: %w", res.err)
	}
	var out []*installedProgram
	for _, line := range strings.Split(decodeOutput([]byte(res.output)), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		if !nameMatches(want, fields[0]) {
			continue
		}
		p := &installedProgram{Name: fields[0], Version: fields[1], PackageID: fields[0]}
		if len(fields) > 2 {
			p.Publisher = fields[2]
		}
		out = append(out, p)
	}
	return out, nil
}

func runPlatformUninstall(ctx context.Context, p *installedProgram, args []string) (int, string, error) {
	if p.PackageID == "" {
		return -1, "", errors.New("no package identity recorded; cannot remove " + p.Name)
	}

	// dpkg removes with -r, which leaves configuration files, and -P, which does
	// not. Compliance work wants the program gone from the machine, so -P is the
	// default and the operator's args are appended after it, not in place of it.
	// rpm's equivalent is -e, and it takes no quiet flag worth guessing at, so
	// the args are the operator's.
	flag := "-r"
	if p.Publisher == "dpkg" {
		flag = "-P"
	}

	tool := "dpkg"
	if p.Publisher != "dpkg" {
		tool = "rpm"
	}

	runArgs := append([]string{flag, p.PackageID}, args...)
	res := runProcess(ctx, tool, runArgs)
	out := decodeOutput([]byte(res.output))
	if res.err != nil {
		return res.exitCode, out, fmt.Errorf("%s %s %s: %w", tool, flag, p.PackageID, res.err)
	}
	return res.exitCode, out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
