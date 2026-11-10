//go:build windows

package systempolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// memReg is an in-memory registry: "<root>|<path>|<name>" -> value. No real
// key on this machine is read or written.
type memReg map[string]string

func rootName(root registry.Key) string {
	switch root {
	case registry.LOCAL_MACHINE:
		return "HKLM"
	case registry.USERS:
		return "HKU"
	}
	return fmt.Sprint(uintptr(root))
}

func (m memReg) id(root registry.Key, path, name string) string {
	return rootName(root) + "|" + strings.ToLower(path) + "|" + strings.ToLower(name)
}

type memKey struct {
	reg  memReg
	root registry.Key
	path string
}

func (k memKey) SetDWordValue(name string, v uint32) error {
	k.reg[k.reg.id(k.root, k.path, name)] = fmt.Sprint(v)
	return nil
}
func (k memKey) SetStringValue(name, v string) error {
	k.reg[k.reg.id(k.root, k.path, name)] = v
	return nil
}
func (k memKey) DeleteValue(name string) error {
	id := k.reg.id(k.root, k.path, name)
	if _, ok := k.reg[id]; !ok {
		return fs.ErrNotExist
	}
	delete(k.reg, id)
	return nil
}
func (k memKey) Close() error { return nil }

type wallpaperFakes struct {
	reg       memReg
	hives     []string
	sessions  map[uint32]string // session -> user SID
	refreshed []uint32
	refreshFn func(uint32) error
}

// useWallpaperFakes routes every registry, session and spawn side effect of
// the wallpaper code through memory for one test.
func useWallpaperFakes(t *testing.T) *wallpaperFakes {
	t.Helper()
	f := &wallpaperFakes{reg: memReg{}, sessions: map[uint32]string{}}
	o1, o2, o3, o4, o5, o6, o7 := openPolicyKey, readRegString, deleteRegKey, listUserHives, activeUserSessions, sessionSID, spawnWallpaperRefresh
	o8 := sleep
	t.Cleanup(func() {
		openPolicyKey, readRegString, deleteRegKey, listUserHives = o1, o2, o3, o4
		activeUserSessions, sessionSID, spawnWallpaperRefresh, sleep = o5, o6, o7, o8
	})
	openPolicyKey = func(root registry.Key, path string) (policyKey, error) {
		return memKey{reg: f.reg, root: root, path: path}, nil
	}
	readRegString = func(root registry.Key, path, name string) (string, error) {
		v, ok := f.reg[f.reg.id(root, path, name)]
		if !ok {
			return "", fs.ErrNotExist
		}
		return v, nil
	}
	deleteRegKey = func(root registry.Key, path string) error {
		prefix := rootName(root) + "|" + strings.ToLower(path) + "|"
		for k := range f.reg {
			if strings.HasPrefix(k, prefix) {
				delete(f.reg, k)
			}
		}
		return nil
	}
	listUserHives = func() ([]string, error) { return f.hives, nil }
	activeUserSessions = func() ([]uint32, error) {
		var ids []uint32
		for id := range f.sessions {
			ids = append(ids, id)
		}
		return ids, nil
	}
	sessionSID = func(id uint32) (string, error) {
		sid, ok := f.sessions[id]
		if !ok {
			return "", errors.New("no such session")
		}
		return sid, nil
	}
	spawnWallpaperRefresh = func(id uint32, _ string) error {
		f.refreshed = append(f.refreshed, id)
		if f.refreshFn != nil {
			return f.refreshFn(id)
		}
		return nil
	}
	sleep = func(time.Duration) {}
	return f
}

func (f *wallpaperFakes) get(root registry.Key, path, name string) (string, bool) {
	v, ok := f.reg[f.reg.id(root, path, name)]
	return v, ok
}

const (
	alice = "S-1-5-21-1-2-3-1001"
	bob   = "S-1-5-21-1-2-3-1002"
	img   = `C:\ProgramData\EndpointManager\corporate-wallpaper.jpg`
)

func TestIsUserSIDKeepsOnlyRealUsers(t *testing.T) {
	for name, want := range map[string]bool{
		alice:                      true,
		"S-1-12-1-111-222-333-444": true, // Entra ID user
		alice + "_Classes":         false,
		".DEFAULT":                 false,
		"S-1-5-18":                 false,
		"S-1-5-19":                 false,
		"S-1-5-20":                 false,
	} {
		if got := isUserSID(name); got != want {
			t.Errorf("isUserSID(%q) = %v, want %v", name, got, want)
		}
	}
}

// The bug: values went to HKLM and HKU\.DEFAULT, which Windows does not read
// for a user's desktop. A locked wallpaper must land in each signed-in user's
// own Policies keys, and their session must be refreshed.
func TestLockedWallpaperIsWrittenToEachUserAndRefreshed(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{alice, bob}
	f.sessions = map[uint32]string{2: alice}
	s := wallpaperState{Path: img, Style: "10", Tile: "0", Locked: true, Digest: "d1"}

	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, sid := range []string{alice, bob} {
		if v, _ := f.get(registry.USERS, sid+`\`+userPolicySystemKey, "Wallpaper"); v != img {
			t.Errorf("%s: policy Wallpaper = %q, want %q", sid, v, img)
		}
		if v, _ := f.get(registry.USERS, sid+`\`+userPolicyActiveDesktop, "NoChangingWallPaper"); v != "1" {
			t.Errorf("%s: NoChangingWallPaper = %q, want 1", sid, v)
		}
		if v, _ := f.get(registry.USERS, sid+`\`+userDesktopKey, "Wallpaper"); v != img {
			t.Errorf("%s: desktop Wallpaper = %q, want %q", sid, v, img)
		}
	}
	for k := range f.reg {
		if strings.HasPrefix(k, "HKLM|software\\microsoft\\windows\\currentversion\\policies") || strings.Contains(k, ".default") {
			t.Errorf("wrote to a location Windows ignores for user desktops: %s", k)
		}
	}
	if len(f.refreshed) != 1 || f.refreshed[0] != 2 {
		t.Errorf("refreshed sessions %v, want [2]", f.refreshed)
	}
}

// Unlocked means "set it, then let the user choose": it is applied once per
// image, not again over the user's own choice at every apply or sign-in.
func TestUnlockedWallpaperIsAppliedOncePerImage(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{alice}
	f.sessions = map[uint32]string{2: alice}
	s := wallpaperState{Path: img, Style: "10", Tile: "0", Digest: "d1"}

	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.get(registry.USERS, alice+`\`+userPolicySystemKey, "Wallpaper"); ok {
		t.Error("an unlocked wallpaper set the enforcing policy value")
	}
	// The user picks their own picture.
	f.reg[f.reg.id(registry.USERS, alice+`\`+userDesktopKey, "Wallpaper")] = `C:\Users\alice\cat.jpg`
	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.get(registry.USERS, alice+`\`+userDesktopKey, "Wallpaper"); v != `C:\Users\alice\cat.jpg` {
		t.Errorf("re-applying the same unlocked wallpaper overrode the user's choice: %q", v)
	}
	if len(f.refreshed) != 1 {
		t.Errorf("refreshed %d times, want once", len(f.refreshed))
	}

	// A new image is a new decision and applies again.
	s.Digest = "d2"
	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.get(registry.USERS, alice+`\`+userDesktopKey, "Wallpaper"); v != img {
		t.Errorf("a new image did not apply: %q", v)
	}
}

// An unlocked wallpaper clears the lock this agent set before, never one a
// domain GPO set for a user this agent has not touched.
func TestUnlockedApplyKeepsAForeignLock(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{bob}
	f.reg[f.reg.id(registry.USERS, bob+`\`+userPolicySystemKey, "Wallpaper")] = `\\corp\gpo.jpg`
	if err := applyWallpaperToUsers(wallpaperState{Path: img, Style: "10", Tile: "0", PolicyStyle: "4", Digest: "d1"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.get(registry.USERS, bob+`\`+userPolicySystemKey, "Wallpaper"); v != `\\corp\gpo.jpg` {
		t.Errorf("an unlocked apply removed a lock this agent did not set: %q", v)
	}
}

// A failed refresh is a failure: the dashboard used to say Enforced while the
// desktop never changed.
func TestFailedRefreshIsReported(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{alice}
	f.sessions = map[uint32]string{2: alice}
	f.refreshFn = func(uint32) error { return errors.New("access denied") }
	err := applyWallpaperToUsers(wallpaperState{Path: img, Style: "10", Tile: "0", Locked: true, Digest: "d1"})
	if err == nil || !strings.Contains(err.Error(), "refresh session 2") {
		t.Fatalf("err = %v, want the refresh failure", err)
	}
}

// Someone signed out when the policy arrived gets it when they sign in.
func TestUserSigningInLaterGetsTheWallpaper(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{alice}
	f.sessions = map[uint32]string{2: alice}
	s := wallpaperState{Path: img, Style: "6", Tile: "0", PolicyStyle: "3", Locked: true, Digest: "d1"}
	if err := saveWallpaperState(s); err != nil {
		t.Fatal(err)
	}
	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatal(err)
	}

	f.hives = append(f.hives, bob)
	f.sessions[3] = bob
	f.refreshed = nil
	ReapplyWallpaperAtLogon(3)

	if v, _ := f.get(registry.USERS, bob+`\`+userPolicySystemKey, "WallpaperStyle"); v != "3" {
		t.Errorf("bob's policy WallpaperStyle = %q, want 3 (Fit in the policy's numbering)", v)
	}
	if v, _ := f.get(registry.USERS, bob+`\`+userDesktopKey, "WallpaperStyle"); v != "6" {
		t.Errorf("bob's desktop WallpaperStyle = %q, want 6 (Fit in Control Panel's numbering)", v)
	}
	if len(f.refreshed) != 1 || f.refreshed[0] != 3 {
		t.Errorf("refreshed %v, want [3]", f.refreshed)
	}
}

// Turning the wallpaper off lifts this agent's lock, now for whoever is signed
// in and later for whoever signs in, and leaves a user it never locked alone.
func TestRevertLiftsOnlyOurLock(t *testing.T) {
	f := useWallpaperFakes(t)
	f.hives = []string{alice}
	f.sessions = map[uint32]string{2: alice}
	s := wallpaperState{Path: img, Style: "10", Tile: "0", Locked: true, Digest: "d1"}
	if err := saveWallpaperState(s); err != nil {
		t.Fatal(err)
	}
	if err := applyWallpaperToUsers(s); err != nil {
		t.Fatal(err)
	}
	// Bob's lock came from somebody else (a domain GPO), not from us.
	f.reg[f.reg.id(registry.USERS, bob+`\`+userPolicySystemKey, "Wallpaper")] = `\\corp\gpo.jpg`

	revertWallpaper()

	if _, ok := f.get(registry.USERS, alice+`\`+userPolicySystemKey, "Wallpaper"); ok {
		t.Error("alice's lock survived the revert")
	}
	if _, ok := f.get(registry.USERS, alice+`\`+userPolicyActiveDesktop, "NoChangingWallPaper"); ok {
		t.Error("alice's NoChangingWallPaper survived the revert")
	}
	if _, ok := loadWallpaperState(); ok {
		t.Error("the saved state survived the revert")
	}

	// Bob was signed out during the revert; at sign-in his hive is checked,
	// and a lock that is not ours stays.
	f.hives = append(f.hives, bob)
	f.sessions[3] = bob
	ReapplyWallpaperAtLogon(3)
	if v, _ := f.get(registry.USERS, bob+`\`+userPolicySystemKey, "Wallpaper"); v != `\\corp\gpo.jpg` {
		t.Errorf("a lock this agent did not set was removed: %q", v)
	}
}

// Earlier versions' HKLM and .DEFAULT values are removed, but only when they
// point at this agent's image.
func TestLegacyValuesRemovedOnlyWhenOurs(t *testing.T) {
	f := useWallpaperFakes(t)
	const sysPol = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	f.reg[f.reg.id(registry.LOCAL_MACHINE, sysPol, "Wallpaper")] = img
	f.reg[f.reg.id(registry.LOCAL_MACHINE, sysPol, "NoChangingWallPaper")] = "1"
	f.reg[f.reg.id(registry.USERS, `.DEFAULT\Control Panel\Desktop`, "Wallpaper")] = `C:\Windows\Web\other.jpg`

	cleanupLegacyWallpaper(img)

	if _, ok := f.get(registry.LOCAL_MACHINE, sysPol, "Wallpaper"); ok {
		t.Error("our legacy HKLM Wallpaper value survived")
	}
	if _, ok := f.get(registry.LOCAL_MACHINE, sysPol, "NoChangingWallPaper"); ok {
		t.Error("our legacy HKLM lock survived")
	}
	if v, _ := f.get(registry.USERS, `.DEFAULT\Control Panel\Desktop`, "Wallpaper"); v != `C:\Windows\Web\other.jpg` {
		t.Errorf(".DEFAULT value not set by us was changed: %q", v)
	}
}

// The policy value and Control Panel number styles differently.
func TestStyleCodes(t *testing.T) {
	for style, want := range map[WallpaperStyle][3]string{
		WallpaperFill: {"10", "0", "4"}, WallpaperFit: {"6", "0", "3"}, WallpaperStretch: {"2", "0", "2"},
		WallpaperCenter: {"0", "0", "0"}, WallpaperTile: {"0", "1", "1"}, "": {"10", "0", "4"},
	} {
		s, tile, pol := wallpaperStyleCodes(style)
		if s != want[0] || tile != want[1] || pol != want[2] {
			t.Errorf("%q -> (%s,%s,%s), want %v", style, s, tile, pol, want)
		}
	}
}
