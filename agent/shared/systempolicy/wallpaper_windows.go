//go:build windows

package systempolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows reads the "Desktop Wallpaper" and "Prevent changing desktop
// background" policies from each user's own hive (HKCU). Earlier versions wrote
// them to HKLM and to HKU\.DEFAULT instead. Windows reads neither for a user's
// desktop -- .DEFAULT is the SYSTEM account's hive, not a template for new
// users -- and SystemParametersInfo called from the service changes Session 0's
// desktop, which nobody sees. The policy reported Enforced and no wallpaper
// changed, not even after signing out and back in.
//
// A standard user cannot write HKCU\...\Policies (its ACL grants the user read
// only), so the service writes each loaded user hive under HKEY_USERS itself.
// Only the visible refresh runs as the user: a short-lived copy of the agent
// started in the user's session (RunWallpaperRefresh). A user who signs in
// later is handled at logon (ReapplyWallpaperAtLogon) from the state saved
// under wallpaperStateKey.

const (
	wallpaperStateKey       = `SOFTWARE\EndpointManager\Wallpaper`
	userPolicySystemKey     = `Software\Microsoft\Windows\CurrentVersion\Policies\System`
	userPolicyActiveDesktop = `Software\Microsoft\Windows\CurrentVersion\Policies\ActiveDesktop`
	userDesktopKey          = `Control Panel\Desktop`
	userMarkerKey           = `Software\EndpointManager`
	userMarkerValue         = "WallpaperApplied"

	// WallpaperRefreshFlag is the internal command-line flag the service passes
	// to the copy of the agent it starts in a user's session.
	WallpaperRefreshFlag = "wallpaper-refresh"
)

// wallpaperState is what the policy decided, saved so a user who signs in
// after the policy was applied gets it too.
type wallpaperState struct {
	Path        string
	Style       string // Control Panel\Desktop WallpaperStyle code
	Tile        string // Control Panel\Desktop TileWallpaper code
	PolicyStyle string // Policies\System WallpaperStyle code: a different numbering
	Locked      bool
	Digest      string // SHA-256 of the image: a new image counts as a new decision
}

// key identifies one decision. The per-user marker stores it, so an unlocked
// wallpaper is set once per decision and a user who changes it afterwards is
// not overridden at every sign-in.
func (s wallpaperState) key() string {
	return strings.Join([]string{s.Path, s.Style, s.Tile, s.Digest}, "|")
}

// The side effects below are variables so the tests run without touching this
// machine's registry, sessions or processes.
var (
	readRegString = func(root registry.Key, path, name string) (string, error) {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
		if err != nil {
			return "", err
		}
		defer k.Close()
		v, _, err := k.GetStringValue(name)
		return v, err
	}
	deleteRegKey = func(root registry.Key, path string) error {
		return registry.DeleteKey(root, path)
	}
	listUserHives         = loadedUserHives
	activeUserSessions    = userSessions
	sessionSID            = userSIDOfSession
	spawnWallpaperRefresh = spawnRefreshAsUser
	sleep                 = time.Sleep
)

// isUserSID keeps real accounts: local and domain users (S-1-5-21-) and
// Microsoft Entra ID users (S-1-12-1-). It drops the service accounts, .DEFAULT
// and each user's _Classes hive.
func isUserSID(name string) bool {
	if strings.HasSuffix(name, "_Classes") {
		return false
	}
	return strings.HasPrefix(name, "S-1-5-21-") || strings.HasPrefix(name, "S-1-12-1-")
}

func loadedUserHives() ([]string, error) {
	k, err := registry.OpenKey(registry.USERS, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if isUserSID(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// wallpaperStyleCodes maps the policy's style to the codes Windows reads.
// The two places number styles differently: Control Panel\Desktop uses
// WallpaperStyle Fill 10, Fit 6, Stretch 2, Center 0 (Tile is 0 with
// TileWallpaper 1), while the Desktop Wallpaper policy under Policies\System
// uses Center 0, Tile 1, Stretch 2, Fit 3, Fill 4. Earlier versions wrote the
// Desktop numbering into the policy value too.
func wallpaperStyleCodes(style WallpaperStyle) (styleCode, tileCode, policyCode string) {
	switch style {
	case WallpaperFit:
		return "6", "0", "3"
	case WallpaperStretch:
		return "2", "0", "2"
	case WallpaperCenter:
		return "0", "0", "0"
	case WallpaperTile:
		return "0", "1", "1"
	default:
		return "10", "0", "4"
	}
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func saveWallpaperState(s wallpaperState) error {
	k, err := openPolicyKey(registry.LOCAL_MACHINE, wallpaperStateKey)
	if err != nil {
		return fmt.Errorf("open %s: %w", wallpaperStateKey, err)
	}
	defer k.Close()
	locked := "0"
	if s.Locked {
		locked = "1"
	}
	var errs regErrs
	errs.add("state Path", k.SetStringValue("Path", s.Path))
	errs.add("state Style", k.SetStringValue("Style", s.Style))
	errs.add("state Tile", k.SetStringValue("Tile", s.Tile))
	errs.add("state PolicyStyle", k.SetStringValue("PolicyStyle", s.PolicyStyle))
	errs.add("state Locked", k.SetStringValue("Locked", locked))
	errs.add("state Digest", k.SetStringValue("Digest", s.Digest))
	return errs.err()
}

func loadWallpaperState() (wallpaperState, bool) {
	path, err := readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "Path")
	if err != nil || path == "" {
		return wallpaperState{}, false
	}
	s := wallpaperState{Path: path}
	s.Style, _ = readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "Style")
	s.Tile, _ = readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "Tile")
	s.PolicyStyle, _ = readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "PolicyStyle")
	s.Digest, _ = readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "Digest")
	locked, _ := readRegString(registry.LOCAL_MACHINE, wallpaperStateKey, "Locked")
	s.Locked = locked == "1"
	return s, true
}

func clearWallpaperState() error {
	if err := deleteRegKey(registry.LOCAL_MACHINE, wallpaperStateKey); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// applyToHive writes one user's hive. wrote says whether anything that user
// sees changed, which is what decides whether their session is refreshed: an
// unlocked wallpaper already applied must not be pushed over the user's own
// choice again.
func applyToHive(sid string, s wallpaperState) (wrote bool, err error) {
	var errs regErrs
	base := sid + `\`

	if s.Locked {
		if k, err := openPolicyKey(registry.USERS, base+userPolicySystemKey); err != nil {
			errs.add("open user Policies\\System", err)
		} else {
			errs.add("policy Wallpaper", k.SetStringValue("Wallpaper", s.Path))
			errs.add("policy WallpaperStyle", k.SetStringValue("WallpaperStyle", s.PolicyStyle))
			k.Close()
		}
		if k, err := openPolicyKey(registry.USERS, base+userPolicyActiveDesktop); err != nil {
			errs.add("open user Policies\\ActiveDesktop", err)
		} else {
			errs.add("NoChangingWallPaper", k.SetDWordValue("NoChangingWallPaper", 1))
			k.Close()
		}
	} else {
		removeUserPolicyValues(sid, &errs)
	}

	marker, _ := readRegString(registry.USERS, base+userMarkerKey, userMarkerValue)
	if s.Locked || marker != s.key() {
		if k, err := openPolicyKey(registry.USERS, base+userDesktopKey); err != nil {
			errs.add("open user desktop", err)
		} else {
			errs.add("Wallpaper", k.SetStringValue("Wallpaper", s.Path))
			errs.add("WallpaperStyle", k.SetStringValue("WallpaperStyle", s.Style))
			errs.add("TileWallpaper", k.SetStringValue("TileWallpaper", s.Tile))
			k.Close()
		}
		if k, err := openPolicyKey(registry.USERS, base+userMarkerKey); err != nil {
			errs.add("open user marker", err)
		} else {
			errs.add("marker", k.SetStringValue(userMarkerValue, s.key()))
			k.Close()
		}
		wrote = true
	}
	return wrote, errs.err()
}

// removeUserPolicyValues drops the lock this agent set for one user. It is
// gated on the marker so a user nobody locked is left alone.
func removeUserPolicyValues(sid string, errs *regErrs) {
	base := sid + `\`
	if marker, err := readRegString(registry.USERS, base+userMarkerKey, userMarkerValue); err != nil || marker == "" {
		return
	}
	if k, err := openPolicyKey(registry.USERS, base+userPolicySystemKey); err == nil {
		errs.add("delete policy Wallpaper", k.DeleteValue("Wallpaper"))
		errs.add("delete policy WallpaperStyle", k.DeleteValue("WallpaperStyle"))
		k.Close()
	}
	if k, err := openPolicyKey(registry.USERS, base+userPolicyActiveDesktop); err == nil {
		errs.add("delete NoChangingWallPaper", k.DeleteValue("NoChangingWallPaper"))
		k.Close()
	}
}

// releaseHive undoes this agent's wallpaper for one user when the policy no
// longer sets one. The user keeps the picture on screen; it just stops being
// locked, and the marker goes so a later policy applies afresh.
func releaseHive(sid string) error {
	base := sid + `\`
	var errs regErrs
	removeUserPolicyValues(sid, &errs)
	if k, err := openPolicyKey(registry.USERS, base+userMarkerKey); err == nil {
		errs.add("delete marker", k.DeleteValue(userMarkerValue))
		k.Close()
	}
	return errs.err()
}

// applyWallpaperToUsers writes every signed-in user's hive and refreshes the
// sessions whose wallpaper changed. With nobody signed in there is nothing to
// write yet: the saved state is applied at the next sign-in.
func applyWallpaperToUsers(s wallpaperState) error {
	var errs regErrs
	hives, err := listUserHives()
	if err != nil {
		errs.add("list user profiles", err)
	}
	wrote := map[string]bool{}
	for _, sid := range hives {
		w, err := applyToHive(sid, s)
		errs.add("user "+sid, err)
		wrote[sid] = w
	}

	sessions, err := activeUserSessions()
	if err != nil {
		errs.add("list sessions", err)
	}
	for _, id := range sessions {
		sid, err := sessionSID(id)
		if err != nil {
			errs.add(fmt.Sprintf("session %d user", id), err)
			continue
		}
		if !wrote[sid] {
			continue
		}
		errs.add(fmt.Sprintf("refresh session %d", id), spawnWallpaperRefresh(id, s.Path))
	}
	return errs.err()
}

// cleanupLegacyWallpaper removes what earlier versions wrote outside the user
// hives. Values are removed only when they point at this agent's own image, so
// a value somebody else set is never touched.
func cleanupLegacyWallpaper(ourPath string) {
	const sysPol = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	if v, err := readRegString(registry.LOCAL_MACHINE, sysPol, "Wallpaper"); err == nil && strings.EqualFold(v, ourPath) {
		if k, err := openPolicyKey(registry.LOCAL_MACHINE, sysPol); err == nil {
			_ = k.DeleteValue("Wallpaper")
			_ = k.DeleteValue("WallpaperStyle")
			_ = k.DeleteValue("NoChangingWallPaper")
			_ = k.DeleteValue("NoChangingWallpaper")
			k.Close()
		}
		if k, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\ActiveDesktop`); err == nil {
			_ = k.DeleteValue("NoChangingWallPaper")
			_ = k.DeleteValue("NoChangingWallpaper")
			k.Close()
		}
	}
	if v, err := readRegString(registry.USERS, `.DEFAULT\Control Panel\Desktop`, "Wallpaper"); err == nil && strings.EqualFold(v, ourPath) {
		if k, err := openPolicyKey(registry.USERS, `.DEFAULT\Control Panel\Desktop`); err == nil {
			_ = k.DeleteValue("Wallpaper")
			_ = k.DeleteValue("WallpaperStyle")
			_ = k.DeleteValue("TileWallpaper")
			k.Close()
		}
	}
}

// ReapplyWallpaperAtLogon runs when a user signs in: the hive of someone who
// was signed out when the policy arrived is loaded only now.
func ReapplyWallpaperAtLogon(sessionID uint32) {
	sid, err := sessionSID(sessionID)
	if err != nil {
		log.Warn().Err(err).Uint32("session", sessionID).Msg("wallpaper: cannot resolve the user who signed in")
		return
	}
	if !waitForHive(sid) {
		log.Warn().Str("sid", sid).Msg("wallpaper: the user's profile did not load; applied at their next sign-in")
		return
	}
	s, ok := loadWallpaperState()
	if !ok {
		if err := releaseHive(sid); err != nil {
			log.Warn().Err(err).Str("sid", sid).Msg("wallpaper: release user lock")
		}
		return
	}
	wrote, err := applyToHive(sid, s)
	if err != nil {
		log.Warn().Err(err).Str("sid", sid).Msg("wallpaper: apply at sign-in")
	}
	if wrote {
		if err := spawnWallpaperRefresh(sessionID, s.Path); err != nil {
			log.Warn().Err(err).Uint32("session", sessionID).Msg("wallpaper: refresh at sign-in")
		}
	}
}

// ReapplyWallpaperAtStartup covers the users already signed in when the
// service starts, after an agent update or a service restart.
func ReapplyWallpaperAtStartup() {
	s, ok := loadWallpaperState()
	if !ok {
		hives, _ := listUserHives()
		for _, sid := range hives {
			_ = releaseHive(sid)
		}
		return
	}
	if err := applyWallpaperToUsers(s); err != nil {
		log.Warn().Err(err).Msg("wallpaper: reapply at service start")
	}
}

// waitForHive gives the profile service time to load a user's hive: the
// sign-in notification can arrive before it is under HKEY_USERS.
func waitForHive(sid string) bool {
	for i := 0; i < 60; i++ {
		hives, err := listUserHives()
		if err == nil {
			for _, h := range hives {
				if strings.EqualFold(h, sid) {
					return true
				}
			}
		}
		sleep(500 * time.Millisecond)
	}
	return false
}

func userSessions() ([]uint32, error) {
	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &count); err != nil {
		return nil, fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	if sessions == nil || count == 0 {
		return nil, nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))
	var out []uint32
	for _, s := range unsafe.Slice(sessions, count) {
		// A disconnected session (fast user switching) still has a desktop
		// the user returns to.
		if s.SessionID != 0 && (s.State == windows.WTSActive || s.State == windows.WTSDisconnected) {
			out = append(out, s.SessionID)
		}
	}
	return out, nil
}

func userSIDOfSession(sessionID uint32) (string, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &tok); err != nil {
		return "", fmt.Errorf("WTSQueryUserToken(session %d): %w", sessionID, err)
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// spawnRefreshAsUser starts this agent with -wallpaper-refresh as the
// session's user, on that user's desktop, and waits for it. It runs with the
// user's own token, not SYSTEM's: SystemParametersInfo acts for the user whose
// process calls it.
func spawnRefreshAsUser(sessionID uint32, path string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent executable: %w", err)
	}
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &tok); err != nil {
		return fmt.Errorf("WTSQueryUserToken(session %d): %w", sessionID, err)
	}
	defer tok.Close()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer primary.Close()

	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, primary, false); err != nil {
		return fmt.Errorf("CreateEnvironmentBlock: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)

	// Neither path can carry a quote or end in a backslash (the executable
	// path and the agent's fixed image path), so plain quoting is exact here.
	cmd := fmt.Sprintf(`"%s" -%s "%s"`, exe, WallpaperRefreshFlag, path)
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cmdPtr, err := windows.UTF16PtrFromString(cmd)
	if err != nil {
		return err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desktop,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcessAsUser(primary, exePtr, cmdPtr, nil, nil, false, flags, env, nil, &si, &pi); err != nil {
		return fmt.Errorf("CreateProcessAsUser in session %d: %w", sessionID, err)
	}
	defer windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)

	// Wait for the answer so a refresh that failed is reported as failed,
	// rather than the setting showing Enforced over an unchanged desktop.
	ev, err := windows.WaitForSingleObject(pi.Process, 15000)
	if err != nil {
		return fmt.Errorf("wait for refresh: %w", err)
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("wallpaper refresh in session %d did not finish within 15s", sessionID)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return fmt.Errorf("read refresh exit code: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("wallpaper refresh in session %d exited with code %d", sessionID, code)
	}
	return nil
}

// RunWallpaperRefresh is the whole of the process spawnRefreshAsUser starts:
// set the signed-in user's wallpaper to path, now, on their desktop.
func RunWallpaperRefresh(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	spi := windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")
	// SPI_SETDESKWALLPAPER, with SPIF_UPDATEINIFILE | SPIF_SENDCHANGE.
	r, _, callErr := spi.Call(0x0014, 0, uintptr(unsafe.Pointer(p)), 0x01|0x02)
	if r == 0 {
		return fmt.Errorf("SystemParametersInfo(SPI_SETDESKWALLPAPER, %q): %w", path, callErr)
	}
	return nil
}
