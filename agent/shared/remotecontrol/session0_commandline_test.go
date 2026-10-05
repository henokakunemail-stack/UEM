//go:build windows

package remotecontrol

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The worker is started with no shell to interpret anything for it. What decides
// its arguments is the Go runtime building os.Args inside the worker, and Go
// parses the command line itself. So that is what these tests check against:
// a real Go child, handed the exact command line SpawnSessionWorker emits and
// asked what it received.
//
// windows.DecomposeCommandLine (shell32) looks like the obvious oracle and is
// the wrong one. It reports plain, unescaped quoting as correct for a path
// ending in a backslash, and that answer is wrong here -- the worker is a Go
// binary, not a shell32 process. A test built on it would pass while the worker
// receives "C:\EndpointAgent\"" for the path C:\EndpointAgent\.

// argvDumpEnv makes this test binary usable as its own oracle. Re-executed with
// it set, the process prints os.Args as JSON and exits before testing parses any
// flags -- necessary because the arguments under test are -rc-worker and
// -rc-creds, which the testing package would reject as unknown flags.
const argvDumpEnv = "EP_AGENT_ARGV_DUMP"

func TestMain(m *testing.M) {
	if os.Getenv(argvDumpEnv) != "" {
		b, err := json.Marshal(os.Args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Stdout.Write(append(b, '\n'))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// goChildArgv runs this test binary as the oracle and returns the os.Args it
// actually saw.
func goChildArgv(t *testing.T, oracle string, cmdline string) []string {
	t.Helper()

	linePtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		t.Fatalf("command line is not representable: %v", err)
	}
	exePtr, err := windows.UTF16PtrFromString(oracle)
	if err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "argv.json")
	// SECURITY_ATTRIBUTES with bInheritHandle, because CreateProcess hands the
	// child the StdOutput/StdErr handles and a handle opened with a nil
	// SECURITY_ATTRIBUTES is not inheritable. Without this the child starts with
	// an invalid stdout, its writes vanish, and the oracle returns an empty file
	// -- which reads as "Go parsed nothing", the opposite of the truth.
	sa := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1, // TRUE
	}
	outFile, err := windows.CreateFile(windows.StringToUTF16Ptr(outPath),
		windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&sa, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	defer windows.CloseHandle(outFile)

	si := windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Flags:     windows.STARTF_USESTDHANDLES,
		StdOutput: outFile,
		StdErr:    outFile,
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(exePtr, linePtr, nil, nil, true,
		windows.CREATE_NO_WINDOW, nil, nil, &si, &pi); err != nil {
		t.Fatalf("CreateProcess(%q): %v", cmdline, err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if wait, _ := windows.WaitForSingleObject(pi.Process, 60000); wait != uint32(windows.WAIT_OBJECT_0) {
		t.Fatalf("oracle did not exit (wait=%d)", wait)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read oracle output: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatalf("oracle output %q: %v", raw, err)
	}
	return got
}

// The properties that matter for a real install, checked against a real Go
// child. Program Files is the ordinary case, not an edge case; the
// trailing-backslash cases are the ones shell32 and Go disagree about.
func TestWorkerCommandLineReachesAGoChildIntact(t *testing.T) {
	oracle, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path on this host: %v", err)
	}
	t.Setenv(argvDumpEnv, "1")

	credsCases := []struct{ name, creds string }{
		{"program files", `C:\Program Files\EndpointAgent\creds.json`},
		{"data dir", `C:\ProgramData\EndpointAgent\creds.json`},
		{"no spaces", `C:\creds.json`},
		{"trailing backslash", `C:\`},
		{"trailing backslash after space", `C:\Program Files\`},
		{"doubled trailing backslash", `C:\a\\`},
	}

	want := SessionConfig{
		SessionID: "abc123",
		Mode:      "full_control",
		RelayURL:  "/api/agent/devices/dev-1/remotecontrol/ws?session=abc123",
	}

	for _, tc := range credsCases {
		t.Run(tc.name, func(t *testing.T) {
			line, err := workerCommandLine(oracle, tc.creds, want)
			if err != nil {
				t.Fatalf("workerCommandLine: %v", err)
			}
			got := goChildArgv(t, oracle, line)

			if len(got) != 5 {
				t.Fatalf("child received %d arguments, want 5: %q", len(got), got)
			}
			if got[0] != oracle {
				t.Errorf("argv[0] = %q, want %q", got[0], oracle)
			}
			if got[1] != "-rc-worker" {
				t.Errorf("argv[1] = %q, want -rc-worker", got[1])
			}
			if got[3] != "-rc-creds" {
				t.Errorf("argv[3] = %q, want -rc-creds", got[3])
			}
			// The assertion that caught the real bug: a corrupted credentials
			// path means the worker opens the wrong file, or none.
			if got[4] != tc.creds {
				t.Errorf("argv[4] = %q, want %q", got[4], tc.creds)
			}

			raw, err := base64.StdEncoding.DecodeString(got[2])
			if err != nil {
				t.Fatalf("argv[2] is not valid base64: %q", got[2])
			}
			var cfg SessionConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("argv[2] is not valid JSON: %v", err)
			}
			if cfg != want {
				t.Errorf("child would run %+v, caller sent %+v", cfg, want)
			}
		})
	}
}

// A payload that would be read as a flag rather than as this flag's value makes
// the worker come up with no session config and no complaint. base64 of JSON
// never begins with "-", but if the encoding ever changed this has to fail loudly
// instead of producing a worker that dials nothing.
func TestWorkerFlagNamesMatchTheWorkerMain(t *testing.T) {
	oracle, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path on this host: %v", err)
	}
	t.Setenv(argvDumpEnv, "1")

	line, err := workerCommandLine(oracle, `C:\ProgramData\EndpointAgent\creds.json`,
		SessionConfig{SessionID: "s1", Mode: "full_control", RelayURL: "/x"})
	if err != nil {
		t.Fatalf("workerCommandLine: %v", err)
	}
	args := goChildArgv(t, oracle, line)
	if len(args) < 5 {
		t.Fatalf("child received %d arguments, want at least 5: %q", len(args), args)
	}

	// Reproduces the worker's own entry point. A flag-name mismatch is invisible
	// at build time and fatal at run time: Go's flag package prints to stderr and
	// exits 2, so the worker dies before dialling, the service falls back to the
	// Session-0 path the bridge exists to avoid, and the operator sees exactly
	// the bug it was built to remove with nothing in any log saying why.
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var gotCfg, gotCreds string
	fs.StringVar(&gotCfg, "rc-worker", "", "")
	fs.StringVar(&gotCreds, "rc-creds", "", "")

	if err := fs.Parse(args[1:]); err != nil {
		t.Fatalf("worker could not parse the command line it was given: %v", err)
	}
	if gotCfg == "" {
		t.Error("worker parsed an empty session config argument")
	}
	if strings.HasPrefix(gotCfg, "-") {
		t.Errorf("session config %q would be read as a flag", gotCfg)
	}
	if gotCreds != `C:\ProgramData\EndpointAgent\creds.json` {
		t.Errorf("credentials path parsed as %q", gotCreds)
	}
}

// The device secret must not be anywhere in the command line. A command line is
// visible to every process that can enumerate the session and is quoted verbatim
// in support logs by anything that dumps them. It travels in the credentials
// file, which the worker opens for itself.
func TestWorkerCommandLineCarriesNoSecret(t *testing.T) {
	line, err := workerCommandLine(`C:\a.exe`, `C:\ProgramData\EndpointAgent\creds.json`,
		SessionConfig{SessionID: "s1", RelayURL: "/x"})
	if err != nil {
		t.Fatalf("workerCommandLine: %v", err)
	}
	lowered := strings.ToLower(line)
	for _, forbidden := range []string{"secret", "token", "password"} {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("command line mentions %q: %q", forbidden, line)
		}
	}
}
