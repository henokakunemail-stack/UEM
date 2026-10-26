//go:build windows

package systempolicy

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The managed local administrator used to be provisioned by a PowerShell
// script: New-LocalUser and Add-LocalGroupMember where the LocalAccounts module
// loaded, net.exe where it did not. The net.exe branch threw its error text
// away (2>$null) and was not repeatable -- on every sync after the first the
// account already existed and was already an administrator, `net localgroup
// /add` failed with "already a member", and the policy was reported as failed
// with "exit status 1 (output: )" and nothing to say why. Which branch ran
// depended on how PowerShell found its modules under the agent's service
// account, which is not something a policy should depend on.
//
// The account is now managed through the Network Management API that both of
// those wrap: the same calls on every Windows version, the password never
// leaves this process, an existing account and an existing membership are the
// expected state rather than errors, and a refusal comes back as a specific
// status the console can show -- most often the domain's password policy.

var (
	modNetapi32 = windows.NewLazySystemDLL("netapi32.dll")

	procNetUserAdd              = modNetapi32.NewProc("NetUserAdd")
	procNetUserGetInfo          = modNetapi32.NewProc("NetUserGetInfo")
	procNetUserSetInfo          = modNetapi32.NewProc("NetUserSetInfo")
	procNetLocalGroupAddMembers = modNetapi32.NewProc("NetLocalGroupAddMembers")
	procNetAPIBufferFree        = modNetapi32.NewProc("NetApiBufferFree")
)

const (
	ufScript           = 0x0001
	ufAccountDisable   = 0x0002
	ufNormalAccount    = 0x0200
	ufDontExpirePasswd = 0x10000

	userPrivUser = 1

	nerrUserNotFound     = 2221
	nerrUserExists       = 2224
	nerrGroupNotFound    = 2220
	nerrPasswordTooShort = 2245
	errorMemberInAlias   = 1378
	errorNoSuchAlias     = 1376
	errorAccessDenied    = 5
	errorInvalidPassword = 86
)

// errAlreadyMember is the account already being in the group: the state the
// policy asks for, not a failure.
var errAlreadyMember = errors.New("already a member")

// netErr turns a NET_API_STATUS into an error an operator can act on.
func netErr(op string, status uintptr) error {
	switch status {
	case 0:
		return nil
	case nerrPasswordTooShort, errorInvalidPassword:
		return fmt.Errorf("%s: the password does not meet this computer's password policy "+
			"(minimum length, complexity or history; on a domain-joined PC the domain policy applies)", op)
	case errorAccessDenied:
		return fmt.Errorf("%s: access denied (the agent is not running as SYSTEM or an administrator)", op)
	case nerrUserNotFound:
		return fmt.Errorf("%s: the account does not exist", op)
	case nerrUserExists:
		return fmt.Errorf("%s: an account with that name already exists", op)
	case nerrGroupNotFound, errorNoSuchAlias:
		return fmt.Errorf("%s: the local Administrators group was not found", op)
	}
	return fmt.Errorf("%s failed: %w", op, syscall.Errno(status))
}

// userInfo1 is USER_INFO_1.
type userInfo1 struct {
	Name        *uint16
	Password    *uint16
	PasswordAge uint32
	Priv        uint32
	HomeDir     *uint16
	Comment     *uint16
	Flags       uint32
	ScriptPath  *uint16
}

// localAccounts is the account API the policy needs. netapiAccounts is the
// real one; tests use a fake to exercise the logic without administrator
// rights.
type localAccounts interface {
	// lookup reports whether the account exists and, if it does, its flags.
	lookup(name string) (exists bool, flags uint32, err error)
	create(name, password, comment string, flags uint32) error
	setPassword(name, password string) error
	setFlags(name string, flags uint32) error
	setFullName(name, fullName string) error
	setComment(name, comment string) error
	adminGroup() (string, error)
	// addToGroup returns errAlreadyMember when the account is already in it.
	addToGroup(group, member string) error
}

// provisionAdmin brings the managed account to the state the policy describes:
// present, enabled, with this password, in the local Administrators group.
// Running it again against an account already in that state succeeds.
func provisionAdmin(a localAccounts, name, password, fullName, comment string, neverExpires bool) error {
	exists, flags, err := a.lookup(name)
	if err != nil {
		return err
	}
	if !exists {
		f := uint32(ufScript | ufNormalAccount)
		if neverExpires {
			f |= ufDontExpirePasswd
		}
		if err := a.create(name, password, comment, f); err != nil {
			return err
		}
	} else {
		if err := a.setPassword(name, password); err != nil {
			return err
		}
		want := flags &^ ufAccountDisable
		if neverExpires {
			want |= ufDontExpirePasswd
		} else {
			want &^= ufDontExpirePasswd
		}
		if want != flags {
			if err := a.setFlags(name, want); err != nil {
				return err
			}
		}
		if err := a.setComment(name, comment); err != nil {
			return err
		}
	}
	if err := a.setFullName(name, fullName); err != nil {
		return err
	}
	group, err := a.adminGroup()
	if err != nil {
		return err
	}
	if err := a.addToGroup(group, name); err != nil && !errors.Is(err, errAlreadyMember) {
		return err
	}
	return nil
}

// netapiAccounts is localAccounts over netapi32.
type netapiAccounts struct{}

func (netapiAccounts) lookup(name string) (bool, uint32, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, 0, err
	}
	var buf *userInfo1
	status, _, _ := procNetUserGetInfo.Call(0, uintptr(unsafe.Pointer(n)), 1, uintptr(unsafe.Pointer(&buf)))
	if status == nerrUserNotFound {
		return false, 0, nil
	}
	if status != 0 {
		return false, 0, netErr(fmt.Sprintf("look up local account %q", name), status)
	}
	flags := buf.Flags
	procNetAPIBufferFree.Call(uintptr(unsafe.Pointer(buf)))
	return true, flags, nil
}

func (netapiAccounts) create(name, password, comment string, flags uint32) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return err
	}
	c, err := windows.UTF16PtrFromString(comment)
	if err != nil {
		return err
	}
	info := userInfo1{Name: n, Password: p, Priv: userPrivUser, Comment: c, Flags: flags}
	var parmErr uint32
	status, _, _ := procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&parmErr)))
	return netErr(fmt.Sprintf("create local account %q", name), status)
}

// setInfo calls NetUserSetInfo with one of the single-field levels.
func setInfo(name string, level uintptr, buf unsafe.Pointer, what string) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var parmErr uint32
	status, _, _ := procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(n)), level, uintptr(buf), uintptr(unsafe.Pointer(&parmErr)))
	return netErr(fmt.Sprintf("set %s of local account %q", what, name), status)
}

func (netapiAccounts) setPassword(name, password string) error {
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return err
	}
	info := struct{ Password *uint16 }{p} // USER_INFO_1003
	return setInfo(name, 1003, unsafe.Pointer(&info), "password")
}

func (netapiAccounts) setFlags(name string, flags uint32) error {
	info := struct{ Flags uint32 }{flags} // USER_INFO_1008
	return setInfo(name, 1008, unsafe.Pointer(&info), "flags")
}

func (netapiAccounts) setFullName(name, fullName string) error {
	f, err := windows.UTF16PtrFromString(fullName)
	if err != nil {
		return err
	}
	info := struct{ FullName *uint16 }{f} // USER_INFO_1011
	return setInfo(name, 1011, unsafe.Pointer(&info), "full name")
}

func (netapiAccounts) setComment(name, comment string) error {
	c, err := windows.UTF16PtrFromString(comment)
	if err != nil {
		return err
	}
	info := struct{ Comment *uint16 }{c} // USER_INFO_1007
	return setInfo(name, 1007, unsafe.Pointer(&info), "description")
}

// adminGroup is the local Administrators group's name in this Windows
// language ("Administrators", "Administrateurs", ...), from its well-known SID.
func (netapiAccounts) adminGroup() (string, error) {
	sid, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return "", err
	}
	account, _, _, err := sid.LookupAccount("")
	if err != nil {
		return "", fmt.Errorf("resolve the local Administrators group: %w", err)
	}
	return account, nil
}

func (netapiAccounts) addToGroup(group, member string) error {
	g, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return err
	}
	m, err := windows.UTF16PtrFromString(member)
	if err != nil {
		return err
	}
	info := struct{ DomainAndName *uint16 }{m} // LOCALGROUP_MEMBERS_INFO_3
	status, _, _ := procNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(g)), 3, uintptr(unsafe.Pointer(&info)), 1)
	if status == errorMemberInAlias {
		return errAlreadyMember
	}
	return netErr(fmt.Sprintf("add %q to %s", member, group), status)
}
