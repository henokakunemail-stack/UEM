package x11

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Candidate is a display some process on the machine is using, with the
// Xauthority file that process authenticates with.
type Candidate struct {
	Display    string
	Xauthority string
	UID        int
	priority   int
}

// Sessions is what a /proc scan found.
type Sessions struct {
	Candidates []Candidate
	// AuthFiles are the -auth files X servers were started with. The display
	// manager's cookie in them opens the display even when no client process
	// names its own XAUTHORITY (the login screen, for one).
	AuthFiles []string
	// Wayland is set when a graphical session runs on Wayland. Its X clients
	// talk to Xwayland, which cannot see the desktop, so capture through X
	// would be black.
	Wayland bool
}

// FindSessions scans procRoot (normally /proc) for processes that have a
// local DISPLAY, and for X servers' -auth files. Candidates come ordered: an
// X11 user session first, then any other user's display (a login screen), then
// root's.
func FindSessions(procRoot string) Sessions {
	var s Sessions
	seen := map[string]bool{}
	entries, _ := os.ReadDir(procRoot)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())

		if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			if auth := xserverAuth(cmd); auth != "" && !seen["auth:"+auth] {
				seen["auth:"+auth] = true
				s.AuthFiles = append(s.AuthFiles, auth)
			}
		}

		raw, err := os.ReadFile(filepath.Join(dir, "environ"))
		if err != nil || len(raw) == 0 {
			continue
		}
		env := parseEnviron(raw)
		if env["XDG_SESSION_TYPE"] == "wayland" || env["WAYLAND_DISPLAY"] != "" {
			s.Wayland = true
			continue
		}
		disp := env["DISPLAY"]
		if disp == "" {
			continue
		}
		if _, _, err := ParseDisplay(disp); err != nil {
			continue
		}
		xauth := env["XAUTHORITY"]
		if xauth == "" && env["HOME"] != "" {
			xauth = filepath.Join(env["HOME"], ".Xauthority")
		}
		uid := procUID(dir)
		key := disp + "\x00" + xauth
		if seen[key] {
			continue
		}
		seen[key] = true
		p := 1
		if uid > 0 {
			p = 2
			if env["XDG_SESSION_TYPE"] == "x11" {
				p = 3
			}
		}
		s.Candidates = append(s.Candidates, Candidate{Display: disp, Xauthority: xauth, UID: uid, priority: p})
	}
	sort.SliceStable(s.Candidates, func(i, j int) bool { return s.Candidates[i].priority > s.Candidates[j].priority })
	return s
}

func parseEnviron(raw []byte) map[string]string {
	env := map[string]string{}
	for _, kv := range bytes.Split(raw, []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if ok {
			env[k] = v
		}
	}
	return env
}

// xserverAuth returns the -auth argument of an Xorg/X/Xvfb command line.
func xserverAuth(cmdline []byte) string {
	args := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
	if len(args) == 0 {
		return ""
	}
	switch filepath.Base(string(args[0])) {
	case "Xorg", "X", "Xvfb", "Xorg.bin":
	default:
		return ""
	}
	for i := 1; i+1 < len(args); i++ {
		if string(args[i]) == "-auth" {
			return string(args[i+1])
		}
	}
	return ""
}

// procUID reads the real uid from /proc/<pid>/status; -1 when unknown.
func procUID(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				if uid, err := strconv.Atoi(f[0]); err == nil {
					return uid
				}
			}
		}
	}
	return -1
}
