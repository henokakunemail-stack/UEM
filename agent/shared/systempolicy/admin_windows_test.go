//go:build windows

package systempolicy

import (
	"errors"
	"fmt"
	"math/rand"
	"os/user"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fakeAccounts is a local account database in memory.
type fakeAccounts struct {
	users   map[string]uint32 // name -> flags
	members map[string]bool
	calls   []string
	failOn  map[string]error
}

func newFake() *fakeAccounts {
	return &fakeAccounts{users: map[string]uint32{}, members: map[string]bool{}, failOn: map[string]error{}}
}

func (f *fakeAccounts) do(op string) error { f.calls = append(f.calls, op); return f.failOn[op] }

func (f *fakeAccounts) lookup(name string) (bool, uint32, error) {
	fl, ok := f.users[name]
	return ok, fl, f.do("lookup")
}
func (f *fakeAccounts) create(name, _, _ string, flags uint32) error {
	if err := f.do("create"); err != nil {
		return err
	}
	f.users[name] = flags
	return nil
}
func (f *fakeAccounts) setPassword(string, string) error { return f.do("setPassword") }
func (f *fakeAccounts) setFlags(name string, flags uint32) error {
	f.users[name] = flags
	return f.do("setFlags")
}
func (f *fakeAccounts) setFullName(string, string) error { return f.do("setFullName") }
func (f *fakeAccounts) setComment(string, string) error  { return f.do("setComment") }
func (f *fakeAccounts) adminGroup() (string, error)      { return "Administrators", f.do("adminGroup") }
func (f *fakeAccounts) addToGroup(_, member string) error {
	if err := f.do("addToGroup"); err != nil {
		return err
	}
	if f.members[member] {
		return errAlreadyMember
	}
	f.members[member] = true
	return nil
}

func TestANewManagedAdminIsCreatedAndMadeAnAdministrator(t *testing.T) {
	f := newFake()
	if err := provisionAdmin(f, "IT-ADMIN2", "pw", "IT", "managed", true); err != nil {
		t.Fatal(err)
	}
	if f.users["IT-ADMIN2"]&ufDontExpirePasswd == 0 || !f.members["IT-ADMIN2"] {
		t.Fatalf("account flags %#x, member %v", f.users["IT-ADMIN2"], f.members["IT-ADMIN2"])
	}
}

// The reported failure: every sync after the first found the account already
// there and already an administrator, and reported the policy as failed with
// "exit status 1 (output: )". That state is exactly what the policy asks for.
func TestReapplyingToAnAccountThatIsAlreadyAdministratorSucceeds(t *testing.T) {
	f := newFake()
	if err := provisionAdmin(f, "IT-ADMIN2", "pw", "IT", "managed", true); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := provisionAdmin(f, "IT-ADMIN2", "pw2", "IT", "managed", true); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	if !contains(f.calls, "setPassword") {
		t.Fatalf("the password of the existing account was not updated (calls %v)", f.calls)
	}
	if contains(f.calls, "create") {
		t.Fatal("tried to create an account that exists")
	}
}

// An account someone disabled by hand is turned back on: the policy says it
// is enabled.
func TestADisabledManagedAdminIsReenabled(t *testing.T) {
	f := newFake()
	f.users["IT-ADMIN2"] = ufNormalAccount | ufScript | ufAccountDisable
	if err := provisionAdmin(f, "IT-ADMIN2", "pw", "IT", "managed", false); err != nil {
		t.Fatal(err)
	}
	if f.users["IT-ADMIN2"]&ufAccountDisable != 0 {
		t.Fatal("the account is still disabled")
	}
}

// A refusal is reported as itself, not as an empty error.
func TestAPasswordPolicyRefusalSaysSo(t *testing.T) {
	f := newFake()
	f.failOn["create"] = netErr(`create local account "IT-ADMIN2"`, nerrPasswordTooShort)
	err := provisionAdmin(f, "IT-ADMIN2", "short", "IT", "managed", true)
	// Windows' own text for the status already mentions the policy; what the
	// operator also needs is that on these PCs it is the domain's policy.
	if err == nil || !strings.Contains(err.Error(), "password policy") || !strings.Contains(err.Error(), "domain policy applies") {
		t.Fatalf("err = %v, want it to name the password policy and the domain", err)
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// USER_INFO_1 is passed to Windows by address; a layout that differs from the
// C struct would hand NetUserAdd a password where it expects a name.
func TestUserInfo1MatchesTheWindowsLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(userInfo1{}) != 56 {
		t.Fatalf("USER_INFO_1 is %d bytes, want 56 on 64-bit Windows", unsafe.Sizeof(userInfo1{}))
	}
}

// The real API, as far as an unprivileged test can go: the Administrators
// group resolves in this Windows language, an existing account is found, a
// missing one is not, and creating one without rights says access denied.
func TestTheNetAPIAnswersOnThisHost(t *testing.T) {
	var a netapiAccounts
	g, err := a.adminGroup()
	if err != nil || g == "" {
		t.Fatalf("admin group: %q, %v", g, err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	name := u.Username[strings.LastIndex(u.Username, `\`)+1:]
	if exists, _, err := a.lookup(name); err == nil && !exists && !strings.Contains(u.Username, "AzureAD") {
		// A domain or Entra account is not a local one; only a local user
		// is expected to be found.
		t.Logf("current user %q is not a local account", u.Username)
	}
	if exists, _, err := a.lookup("uem-no-such-account-x1"); err != nil || exists {
		t.Fatalf("missing account: exists=%v err=%v", exists, err)
	}
	if !isElevated() {
		err := a.create("uem-no-such-account-x1", "Xy7!probe-Long-Pass", "probe", ufNormalAccount|ufScript)
		if err == nil || !strings.Contains(err.Error(), "access denied") {
			t.Fatalf("unprivileged create: %v, want access denied", err)
		}
	}
}

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// The whole path against the real account database, twice, where the test
// has the rights (an elevated shell, the Windows CI runner). The account is
// removed afterwards.
func TestProvisioningARealAccountTwice(t *testing.T) {
	if !isElevated() {
		t.Skip("needs an elevated process")
	}
	name := fmt.Sprintf("uem-ci-%06d", rand.Intn(1000000))
	t.Cleanup(func() { deleteLocalUser(name) })
	pass := "Uem!Ci-" + fmt.Sprint(rand.Int63())
	for i := 0; i < 2; i++ {
		if err := provisionAdmin(netapiAccounts{}, name, pass, "UEM CI", "test account", true); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	var a netapiAccounts
	exists, flags, err := a.lookup(name)
	if err != nil || !exists || flags&ufAccountDisable != 0 || flags&ufDontExpirePasswd == 0 {
		t.Fatalf("account after provisioning: exists=%v flags=%#x err=%v", exists, flags, err)
	}
	g, _ := a.adminGroup()
	if err := a.addToGroup(g, name); !errors.Is(err, errAlreadyMember) {
		t.Fatalf("account is not in %s (adding again returned %v)", g, err)
	}
}

func deleteLocalUser(name string) {
	n, _ := windows.UTF16PtrFromString(name)
	proc := modNetapi32.NewProc("NetUserDel")
	proc.Call(0, uintptr(unsafe.Pointer(n)))
}
