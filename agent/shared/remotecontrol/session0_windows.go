//go:build windows

package remotecontrol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WTS_CURRENT_SERVER_HANDLE is 0. x/sys/windows does not export it, and it is
// not a magic value that can be guessed at the call site: the handle is a
// server scope, not a real object, so passing a handle value that happens to be
// a valid session id would enumerate something else entirely.
const wtsCurrentServerHandle = 0

// wtsCurrentVersion is the WTSINFOSTRUCT version WTSEnumerateSessions writes
// for each session. Version 1 is the only one the enumeration call understands;
// passing 0 fills a struct whose layout does not match the one we read below.
const wtsCurrentVersion = 1

// interactiveDesktop is what binds the child to the desktop an operator can
// actually see. Without this the child still starts in the user's session, but
// on the service's own invisible desktop, and the capture comes back black —
// the same failure as not spawning it at all, only harder to notice.
const interactiveDesktop = `winsta0\default`

// SpawnSessionWorker starts a second copy of the agent inside the logged-on
// user's interactive session, running as that user.
//
// A Windows Service runs as LocalSystem in Session 0. Window stations belong to
// a session, not to a user, so the service's own process can never be moved onto
// the interactive desktop: SetThreadDesktop fails across the boundary and
// OpenInputDesktop hands back nothing that draws. The fix is not a better API
// call, it is a different process. We take the user's own token from the
// session, spawn ourselves again on winsta0\default, and let that second
// process do the capturing with the same rights a manually-launched agent would
// have. The service keeps only the pid, so teardown stays a TerminateProcess.
func SpawnSessionWorker(exePath string, credsPath string, cfg SessionConfig) (pid uint32, err error) {
	if exePath == "" || credsPath == "" {
		// Both are load-bearing. Without a binary there is no worker, and
		// without the credentials file the worker comes up with no DeviceSecret
		// and is rejected by the relay — which reads as "the endpoint is
		// offline" rather than "we misconfigured the spawn".
		return 0, fmt.Errorf("spawn session worker: exe path and credentials path are both required (exe=%q creds=%q)", exePath, credsPath)
	}

	sessionID, err := activeInteractiveSession()
	if err != nil {
		return 0, err
	}

	tok, err := sessionUserToken(sessionID)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(windows.Handle(tok))

	// WTSQueryUserToken hands back an IMPERSONATION token. CreateProcessAsUser
	// wants a PRIMARY one and rejects the impersonation flavour with an access
	// error that names nothing helpful, so this duplication is the difference
	// between spawning and not spawning. MAXIMUM_ALLOWED rather than a hand-built
	// mask: the exact rights CreateProcessAsUser ends up exercising are not
	// worth enumerating, and the token we start from is already privileged.
	primary, err := primaryToken(tok)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(windows.Handle(primary))

	// The service's own environment is LocalSystem's, not the operator's. A
	// worker launched with it would be missing the user's TEMP, APPDATA and
	// profile variables, and anything that resolves a per-user path on startup
	// resolves it to the wrong place. CreateEnvironmentBlock is the only way to
	// get the block that belongs to this token.
	var envBlock *uint16
	if err := windows.CreateEnvironmentBlock(&envBlock, primary, false); err != nil {
		return 0, fmt.Errorf("spawn session worker: CreateEnvironmentBlock for session %d: %w", sessionID, err)
	}
	defer windows.DestroyEnvironmentBlock(envBlock)

	cmdline, err := workerCommandLine(exePath, credsPath, cfg)
	if err != nil {
		return 0, err
	}
	exePtr, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return 0, fmt.Errorf("spawn session worker: executable path %q: %w", exePath, err)
	}
	cmdlinePtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return 0, fmt.Errorf("spawn session worker: command line %q: %w", cmdline, err)
	}
	desktop, err := windows.UTF16PtrFromString(interactiveDesktop)
	if err != nil {
		return 0, fmt.Errorf("spawn session worker: desktop %q: %w", interactiveDesktop, err)
	}

	// STARTF_USESHOWWINDOW with SW_HIDE is the second half of CREATE_NO_WINDOW:
	// the flag stops a console being allocated, this stops a window being shown
	// on the operator's screen when the worker starts its own UI. Neither is
	// about secrecy — an agent that flashes a black console mid-session is
	// indistinguishable from malware, and the endpoint's owner should not have
	// to notice us at all.
	si := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desktop,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}

	// DETACHED_PROCESS so stopping the service does not take the live session
	// down with it: the operator's screen must survive an agent restart, or the
	// outage that caused the restart becomes the outage they sit through.
	// CREATE_UNICODE_ENVIRONMENT because the block above is UTF-16.
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.DETACHED_PROCESS)
	if err := windows.CreateProcessAsUser(primary, exePtr, cmdlinePtr, nil, nil, false, flags, envBlock, nil, &si, &pi); err != nil {
		return 0, fmt.Errorf("spawn session worker: CreateProcessAsUser %q in session %d: %w", exePath, sessionID, err)
	}

	// Both handles go back immediately. The service does not wait on the child
	// and does not track it beyond the pid — holding a process handle would keep
	// the terminated worker's object alive in the kernel and turn "the pid is
	// stale" from an answer into a wait.
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)

	return pi.ProcessId, nil
}

// KillSessionWorker terminates a worker spawned earlier.
//
// The OpenProcess error is returned verbatim on purpose. A stale pid is the
// normal outcome whenever the worker died on its own or the endpoint rebooted
// mid-session, and it is information, not a failure: the caller is tearing a
// session down, and it should log this and finish rather than treat "it was
// already gone" as an error worth alerting on.
func KillSessionWorker(pid uint32) error {
	if pid == 0 {
		return fmt.Errorf("kill session worker: pid is zero")
	}

	// SYNCHRONIZE rides along so that a process already exiting blocks here
	// rather than racing us into a TerminateProcess that lands on a recycled
	// pid. Without it, terminating during teardown can kill whatever the system
	// handed the next id to.
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return fmt.Errorf("kill session worker: OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)

	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("kill session worker: TerminateProcess(%d): %w", pid, err)
	}
	return nil
}

// activeInteractiveSession returns the first session that has a live, unlocked
// desktop behind it.
//
// Session 0 is excluded explicitly. It is always WTSActive on a machine where the
// service runs, so without the check we would spawn into our own session and
// reproduce the exact black-capture failure this function exists to fix.
func activeInteractiveSession() (uint32, error) {
	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(wtsCurrentServerHandle, 0, wtsCurrentVersion, &sessions, &count); err != nil {
		return 0, fmt.Errorf("spawn session worker: WTSEnumerateSessions: %w", err)
	}
	if sessions != nil {
		// The array is in memory owned by wtsapi32, not by us. Leaking it is
		// a slow leak inside a process that is expected to run for months.
		defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))
	}

	// A null pointer with a non-zero count is not a combination the docs
	// promise cannot happen, and unsafe.Slice panics on it — which would take
	// the service down on the one call made to rescue a wedged session. Fold it
	// into the same "nothing is logged on" answer instead.
	if sessions == nil || count == 0 {
		return 0, fmt.Errorf("spawn session worker: no interactive session is logged on (found %d sessions, none active above session 0)", count)
	}
	items := unsafe.Slice(sessions, count)
	for i := range items {
		s := &items[i]
		if s.SessionID != 0 && s.State == windows.WTSActive {
			return s.SessionID, nil
		}
	}

	// Say what is actually wrong. "No session" reads as a bug report from
	// someone staring at a logged-in machine; the truth is usually fast user
	// switching left the machine on a locked or disconnected session.
	return 0, fmt.Errorf("spawn session worker: no interactive session is logged on (found %d sessions, none active above session 0)", count)
}

// sessionUserToken borrows the logged-on user's access token for a session.
func sessionUserToken(sessionID uint32) (windows.Token, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &tok); err != nil {
		return 0, fmt.Errorf("spawn session worker: WTSQueryUserToken(session %d): %w", sessionID, err)
	}
	return tok, nil
}

// primaryToken converts an impersonation token into a primary one.
func primaryToken(tok windows.Token) (windows.Token, error) {
	var primary windows.Token
	err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary)
	if err != nil {
		return 0, fmt.Errorf("spawn session worker: DuplicateTokenEx to a primary token: %w", err)
	}
	return primary, nil
}

// workerCommandLine builds the single NUL-terminated command line
// CreateProcessAsUser parses. Every path is quoted: the agent's own executable
// lives under "Program Files" on a normal install, and an unquoted space there
// splits argv[0] in half and the worker never starts.
//
// The DeviceSecret is deliberately absent. It travels in the credentials file
// the worker opens for itself, because a command line is visible to every
// process that can enumerate the session and is quoted in support logs by
// anything that dumps them. Base64 here is encoding for argument transport, not
// secrecy — the only thing it protects is characters that would otherwise break
// argv parsing.
func workerCommandLine(exePath string, credsPath string, cfg SessionConfig) (string, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("spawn session worker: encode session config: %w", err)
	}

	// Round-trip through the same UTF-16 form the child will decode, so a
	// relay URL or session id that cannot survive the trip is rejected here
	// instead of turning into a worker that silently connects to nothing.
	encoded := base64.StdEncoding.EncodeToString(payload)
	if _, err := windows.UTF16FromString(encoded); err != nil {
		return "", fmt.Errorf("spawn session worker: encoded config is not representable: %w", err)
	}

	quoted := strings.Join([]string{
		quoteArg(exePath),
		"-rc-worker",
		quoteArg(encoded),
		"-rc-creds",
		quoteArg(credsPath),
	}, " ")

	return quoted, nil
}

// quoteArg wraps one argument in double quotes, escaping the backslashes that
// immediately precede a quote and any interior quote. Without that, a path ending
// in a backslash produces an escaped closing quote and swallows the next
// argument — the classic C-runtime quoting trap, and one that only shows up on
// machines where the install path happens to end in a separator.
//
// The C runtime gives a backslash meaning only when a quote follows it: a run
// before an interior quote is doubled, the quote itself is backslashed, and a run
// at the end of the argument is doubled against the closing quote we are about to
// add. Without that last case an install path ending in a separator escapes our
// own closing quote and swallows the next argument. Runs anywhere else pass
// through unchanged, because doubling them would change the path itself.
//
// Both parsers agree on this rule, which is worth stating because it was
// measured rather than assumed: a real Go child handed these command lines
// through CreateProcess, and shell32's CommandLineToArgvW, produce identical
// argv on every case -- and both corrupt plain quoting the same way. "C:\" then
// arrives as "C:\"", and "C:\a\\" loses a backslash, while this form round-trips
// both. session0_commandline_test.go drives a real Go child for exactly that
// reason: the rule is checked against the parser that decides the worker's
// arguments, not against a model of one.
func quoteArg(arg string) string {
	var b strings.Builder
	b.Grow(len(arg) + 2)
	b.WriteByte('"')
	for i := 0; i < len(arg); i++ {
		switch c := arg[i]; {
		case c == '\\':
			run := 1
			for i+run < len(arg) && arg[i+run] == '\\' {
				run++
			}
			if i+run >= len(arg) || arg[i+run] == '"' {
				run *= 2
			}
			b.WriteString(strings.Repeat(`\`, run))
			i += run - 1
		case c == '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
