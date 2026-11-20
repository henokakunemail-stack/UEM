//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// postureScript reads every posture fact that only WMI/CIM or a PowerShell
// module exposes, in one process so the cost is one PowerShell start per
// collection. Each section catches its own failure and reports the message
// under "<section>_error", so a missing namespace (BitLocker on Home,
// SecurityCenter2 on Server) costs that one control and not the rest.
const postureScript = `$ErrorActionPreference = 'Stop'
$o = [ordered]@{}
try {
  $o.bitlocker = @(Get-CimInstance -Namespace 'root/cimv2/Security/MicrosoftVolumeEncryption' -ClassName Win32_EncryptableVolume | ForEach-Object {
    $pct = $null; $conv = $null
    try { $r = Invoke-CimMethod -InputObject $_ -MethodName GetConversionStatus; $pct = $r.EncryptionPercentage; $conv = $r.ConversionStatus } catch {}
    [ordered]@{ drive = [string]$_.DriveLetter; volume_type = $_.VolumeType; protection = $_.ProtectionStatus; conversion = $conv; percent = $pct; method = $_.EncryptionMethod }
  })
} catch { $o.bitlocker_error = $_.Exception.Message }
try {
  $m = Get-MpComputerStatus
  $o.defender = [ordered]@{ am_service = $m.AMServiceEnabled; antivirus = $m.AntivirusEnabled; real_time = $m.RealTimeProtectionEnabled; signature_age = $m.AntivirusSignatureAge; running_mode = [string]$m.AMRunningMode }
} catch { $o.defender_error = $_.Exception.Message }
try {
  $o.security_center = @(Get-CimInstance -Namespace 'root/SecurityCenter2' -ClassName AntiVirusProduct | ForEach-Object { [ordered]@{ name = [string]$_.displayName; state = [int64]$_.productState } })
} catch { $o.security_center_error = $_.Exception.Message }
try {
  $o.firewall = @(Get-NetFirewallProfile -PolicyStore ActiveStore | ForEach-Object { [ordered]@{ name = [string]$_.Name; enabled = [string]$_.Enabled } })
} catch { $o.firewall_error = $_.Exception.Message }
try { $b = Get-CimInstance -ClassName Win32_BIOS -Property ReleaseDate; if ($b.ReleaseDate) { $o.bios_date = $b.ReleaseDate.ToString('yyyy-MM-dd') } } catch {}
try {
  $o.users = @(Get-CimInstance -ClassName Win32_Process -Filter "Name='explorer.exe'" | ForEach-Object {
    $w = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
    if ($w.User) { "$($w.Domain)\$($w.User)" }
  } | Sort-Object -Unique)
} catch {}
$o | ConvertTo-Json -Compress -Depth 5`

// postureFacts is what postureScript prints.
type postureFacts struct {
	BitLocker      []bitlockerVolume `json:"bitlocker"`
	BitLockerError string            `json:"bitlocker_error"`

	Defender      *defenderStatus `json:"defender"`
	DefenderError string          `json:"defender_error"`

	SecurityCenter      []securityCenterAV `json:"security_center"`
	SecurityCenterError string             `json:"security_center_error"`

	Firewall      []firewallProfileFact `json:"firewall"`
	FirewallError string                `json:"firewall_error"`

	BIOSDate string   `json:"bios_date"`
	Users    []string `json:"users"`
}

type bitlockerVolume struct {
	Drive      string   `json:"drive"`
	VolumeType *int     `json:"volume_type"`
	Protection *int     `json:"protection"`
	Conversion *int     `json:"conversion"`
	Percent    *float64 `json:"percent"`
	Method     *int     `json:"method"`
}

type defenderStatus struct {
	AMService    *bool  `json:"am_service"`
	Antivirus    *bool  `json:"antivirus"`
	RealTime     *bool  `json:"real_time"`
	SignatureAge *int   `json:"signature_age"`
	RunningMode  string `json:"running_mode"`
}

type securityCenterAV struct {
	Name  string `json:"name"`
	State int64  `json:"state"`
}

type firewallProfileFact struct {
	Name    string `json:"name"`
	Enabled string `json:"enabled"`
}

// runPostureScript is a seam for tests.
var runPostureScript = func(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", postureScript)
	return cmd.Output()
}

// CollectPosture gathers the security posture of this machine. It never
// fails as a whole: a control that could not be read is reported as unknown
// with the reason, so the report shows the gap instead of hiding it.
func CollectPosture(ctx context.Context) *protocol.Posture {
	var facts postureFacts
	var scriptErr error
	raw, err := runPostureScript(ctx)
	if err != nil {
		scriptErr = fmt.Errorf("posture script: %w", err)
	} else if err := json.Unmarshal(trimJSON(raw), &facts); err != nil {
		scriptErr = fmt.Errorf("posture script output: %w", err)
	}
	pw, pwErr := readPasswordPolicy()
	lock, lockErr := readInactivityTimeout()
	p := buildPosture(facts, scriptErr, pw, pwErr, lock, lockErr)
	p.LastBootUTC = lastBootUTC()
	return &p
}

func trimJSON(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	return []byte(s)
}

var procGetTickCount64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

// lastBootUTC derives boot time from the tick counter: no WMI, no parsing.
func lastBootUTC() string {
	ms, _, _ := procGetTickCount64.Call()
	up := time.Duration(ms) * time.Millisecond
	return time.Now().Add(-up).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// LastBootUTC is exported for the inventory OS detail.
func LastBootUTC() string { return lastBootUTC() }

// buildPosture turns raw facts into the wire shape. Pure, so it is tested
// against recorded script output without PowerShell.
func buildPosture(f postureFacts, scriptErr error, pw *passwordPolicy, pwErr error, lockSecs *int, lockErr error) protocol.Posture {
	p := protocol.Posture{SchemaVersion: protocol.PostureSchemaVersion}
	if scriptErr != nil {
		reason := scriptErr.Error()
		p.DiskEncryption = protocol.DiskEncryptionPosture{State: protocol.PostureUnknown, Reason: reason}
		p.Antivirus = protocol.AntivirusPosture{State: protocol.PostureUnknown, Reason: reason}
		p.Firewall = protocol.FirewallPosture{State: protocol.PostureUnknown, Reason: reason}
	} else {
		p.DiskEncryption = buildDiskEncryption(f)
		p.Antivirus = buildAntivirus(f)
		p.Firewall = buildFirewall(f)
		p.BIOSReleaseDate = f.BIOSDate
		p.LoggedOnUsers = f.Users
	}
	p.PasswordPolicy = buildPasswordPolicy(pw, pwErr)
	p.ScreenLock = buildScreenLock(lockSecs, lockErr)
	return p
}

// isMissingNamespace is WMI's "Invalid namespace" (0x8004100E): the feature
// is not installed on this edition, as opposed to failing to answer.
func isMissingNamespace(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "invalid namespace") || strings.Contains(m, "0x8004100e")
}

var bitlockerMethods = map[int]string{
	0: "None", 1: "AES 128 with diffuser", 2: "AES 256 with diffuser", 3: "AES 128", 4: "AES 256",
	5: "Hardware encryption", 6: "XTS-AES 128", 7: "XTS-AES 256",
}

var bitlockerConversion = map[int]string{
	0: "FullyDecrypted", 1: "FullyEncrypted", 2: "EncryptionInProgress", 3: "DecryptionInProgress",
	4: "EncryptionPaused", 5: "DecryptionPaused",
}

func buildDiskEncryption(f postureFacts) protocol.DiskEncryptionPosture {
	if f.BitLockerError != "" {
		if isMissingNamespace(f.BitLockerError) {
			return protocol.DiskEncryptionPosture{State: protocol.PostureNotApplicable,
				Reason: "BitLocker is not available on this Windows edition"}
		}
		return protocol.DiskEncryptionPosture{State: protocol.PostureUnknown, Reason: "BitLocker: " + f.BitLockerError}
	}
	var vols []protocol.EncryptedVolume
	for _, v := range f.BitLocker {
		// 0 = OS volume, 1 = fixed data; removable drives (2) come and go and
		// are not part of the machine's posture.
		if v.VolumeType != nil && *v.VolumeType != 0 && *v.VolumeType != 1 {
			continue
		}
		if v.Drive == "" {
			continue // unmounted recovery/EFI partitions
		}
		ev := protocol.EncryptedVolume{
			Mount:            v.Drive,
			System:           v.VolumeType != nil && *v.VolumeType == 0,
			PercentEncrypted: v.Percent,
		}
		if v.Method != nil {
			ev.Method = bitlockerMethods[*v.Method]
		}
		conv := ""
		if v.Conversion != nil {
			conv = bitlockerConversion[*v.Conversion]
		}
		protOn := v.Protection != nil && *v.Protection == 1
		ev.Protected = protOn && v.Conversion != nil && *v.Conversion == 1
		switch {
		case v.Protection == nil || *v.Protection == 2:
			ev.Status = "Unknown (volume locked)"
		case protOn:
			ev.Status = conv
		case v.Conversion != nil && *v.Conversion == 1:
			ev.Status = "FullyEncrypted, protection suspended"
		case conv != "":
			ev.Status = conv
		}
		vols = append(vols, ev)
	}
	if len(vols) == 0 {
		return protocol.DiskEncryptionPosture{State: protocol.PostureUnknown, Reason: "BitLocker reported no fixed volumes"}
	}
	return protocol.DiskEncryptionPosture{State: protocol.PostureReported, Volumes: vols}
}

func buildAntivirus(f postureFacts) protocol.AntivirusPosture {
	scOK := f.SecurityCenterError == ""
	defOK := f.Defender != nil && f.DefenderError == ""
	defPassive := defOK && strings.Contains(strings.ToLower(f.Defender.RunningMode), "passive")

	var products []protocol.AVProduct
	if scOK {
		for _, sc := range f.SecurityCenter {
			// productState packs WSC_SECURITY_PRODUCT_STATE in bits 12-15
			// (0x1000 on, 0x0000 off, 0x2000 snoozed, 0x3000 expired) and the
			// signature status in bits 4-7 (0x00 up to date, 0x10 out of date).
			// Only "on" counts as enabled: a snoozed product is not protecting.
			enabled := sc.State&0xF000 == 0x1000
			upToDate := sc.State&0xF0 == 0
			p := protocol.AVProduct{Name: sc.Name, Enabled: &enabled, UpToDate: &upToDate}
			if defOK && strings.Contains(strings.ToLower(sc.Name), "defender") {
				p.RealTime = f.Defender.RealTime
				p.SignatureAgeDays = f.Defender.SignatureAge
			}
			products = append(products, p)
		}
		return protocol.AntivirusPosture{State: protocol.PostureReported, Products: products}
	}

	// No Security Center: Windows Server, or a broken WMI repository. Only
	// Defender can be read directly; a third-party product cannot be seen.
	if defOK && !defPassive {
		enabled := boolOr(f.Defender.Antivirus, false) && boolOr(f.Defender.AMService, false)
		products = append(products, protocol.AVProduct{
			Name: "Microsoft Defender Antivirus", Enabled: &enabled,
			RealTime: f.Defender.RealTime, SignatureAgeDays: f.Defender.SignatureAge,
		})
		return protocol.AntivirusPosture{State: protocol.PostureReported, Products: products}
	}
	reason := "Security Center: " + f.SecurityCenterError
	switch {
	case defPassive:
		reason += "; Defender is in passive mode, so another antivirus is active but cannot be identified"
	case f.DefenderError != "":
		reason += "; Defender: " + f.DefenderError
	}
	return protocol.AntivirusPosture{State: protocol.PostureUnknown, Reason: reason}
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func buildFirewall(f postureFacts) protocol.FirewallPosture {
	if f.FirewallError != "" {
		return protocol.FirewallPosture{State: protocol.PostureUnknown, Reason: "Firewall: " + f.FirewallError}
	}
	if len(f.Firewall) == 0 {
		return protocol.FirewallPosture{State: protocol.PostureUnknown, Reason: "no firewall profiles reported"}
	}
	var profiles []protocol.FirewallProfile
	for _, fp := range f.Firewall {
		switch strings.ToLower(fp.Enabled) {
		case "true", "1":
			profiles = append(profiles, protocol.FirewallProfile{Name: fp.Name, Enabled: true})
		case "false", "0":
			profiles = append(profiles, protocol.FirewallProfile{Name: fp.Name, Enabled: false})
		default:
			return protocol.FirewallPosture{State: protocol.PostureUnknown,
				Reason: fmt.Sprintf("profile %s reported enabled=%q", fp.Name, fp.Enabled)}
		}
	}
	return protocol.FirewallPosture{State: protocol.PostureReported, Profiles: profiles}
}

// --- local password policy (NetUserModalsGet) ---

type passwordPolicy struct {
	MinLength, MaxAgeDays, History, LockoutThreshold int
}

var procNetUserModalsGet = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserModalsGet")
var procNetAPIBufferFreeP = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetApiBufferFree")

type userModalsInfo0 struct {
	MinPasswdLen    uint32
	MaxPasswdAge    uint32
	MinPasswdAge    uint32
	ForceLogoff     uint32
	PasswordHistLen uint32
}

type userModalsInfo3 struct {
	LockoutDuration          uint32
	LockoutObservationWindow uint32
	LockoutThreshold         uint32
}

const timeqForever = 0xFFFFFFFF

func netUserModalsGet(level uint32) (unsafe.Pointer, error) {
	if err := procNetUserModalsGet.Find(); err != nil {
		return nil, err
	}
	var buf unsafe.Pointer
	rc, _, _ := procNetUserModalsGet.Call(0, uintptr(level), uintptr(unsafe.Pointer(&buf)))
	if rc != 0 {
		return nil, fmt.Errorf("NetUserModalsGet level %d: status %d", level, rc)
	}
	return buf, nil
}

func readPasswordPolicy() (*passwordPolicy, error) {
	b0, err := netUserModalsGet(0)
	if err != nil {
		return nil, err
	}
	i0 := *(*userModalsInfo0)(b0)
	procNetAPIBufferFreeP.Call(uintptr(b0))

	b3, err := netUserModalsGet(3)
	if err != nil {
		return nil, err
	}
	i3 := *(*userModalsInfo3)(b3)
	procNetAPIBufferFreeP.Call(uintptr(b3))

	pp := &passwordPolicy{
		MinLength:        int(i0.MinPasswdLen),
		History:          int(i0.PasswordHistLen),
		LockoutThreshold: int(i3.LockoutThreshold),
	}
	if i0.MaxPasswdAge != timeqForever {
		pp.MaxAgeDays = int(i0.MaxPasswdAge / 86400)
	}
	return pp, nil
}

func buildPasswordPolicy(pw *passwordPolicy, err error) protocol.PasswordPolicyPosture {
	if err != nil || pw == nil {
		reason := "password policy could not be read"
		if err != nil {
			reason = err.Error()
		}
		return protocol.PasswordPolicyPosture{State: protocol.PostureUnknown, Reason: reason}
	}
	minLen, maxAge, hist, lock := pw.MinLength, pw.MaxAgeDays, pw.History, pw.LockoutThreshold
	return protocol.PasswordPolicyPosture{State: protocol.PostureReported,
		MinLength: &minLen, MaxAgeDays: &maxAge, HistoryCount: &hist, LockoutThreshold: &lock}
}

// --- screen lock (machine inactivity limit) ---

// readInactivityTimeout reads "Interactive logon: Machine inactivity limit",
// the one machine-wide lock setting (the screen saver timeout is per user).
// Nil with no error means none is enforced.
func readInactivityTimeout() (*int, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("InactivityTimeoutSecs")
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, nil
	}
	secs := int(v)
	return &secs, nil
}

func buildScreenLock(secs *int, err error) protocol.ScreenLockPosture {
	if err != nil {
		return protocol.ScreenLockPosture{State: protocol.PostureUnknown, Reason: "inactivity limit: " + err.Error()}
	}
	if secs == nil {
		return protocol.ScreenLockPosture{State: protocol.PostureReported}
	}
	req := true // the machine inactivity limit locks the session, which needs credentials
	return protocol.ScreenLockPosture{State: protocol.PostureReported, TimeoutSeconds: secs, RequirePassword: &req}
}
