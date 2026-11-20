// Package compliance evaluates devices against a compliance profile and
// renders the result as an audit-ready PDF.
//
// The one rule everything here follows: a control is only ever "pass" when the
// data to say so was read and is recent. A device the server knows nothing
// about is "unknown", never compliant, and unknowns are counted on their own
// instead of being folded into the score.
package compliance

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// Result of one control on one device.
const (
	StatusPass          = "pass"
	StatusFail          = "fail"
	StatusUnknown       = "unknown"
	StatusNotApplicable = "not_applicable"
)

// Overall device verdicts.
const (
	DeviceCompliant     = "compliant"
	DeviceNonCompliant  = "noncompliant"
	DeviceUnknown       = "unknown"
	DeviceNotApplicable = "not_applicable"
)

// Control identifiers, in report order.
const (
	ControlDiskEncryption = "disk_encryption"
	ControlAntivirus      = "antivirus"
	ControlFirewall       = "firewall"
	ControlPatches        = "patches"
	ControlPasswordPolicy = "password_policy"
	ControlScreenLock     = "screen_lock"
)

// AllControls is the fixed report order.
var AllControls = []string{
	ControlDiskEncryption, ControlAntivirus, ControlFirewall,
	ControlPatches, ControlPasswordPolicy, ControlScreenLock,
}

// Rules are a profile's controls and thresholds. A zero threshold switches that
// particular check off; a false Require* switches the whole control off.
type Rules struct {
	RequireDiskEncryption bool `json:"require_disk_encryption"`

	RequireAntivirus bool `json:"require_antivirus"`
	RequireRealTime  bool `json:"require_real_time"`
	// MaxSignatureAgeDays 0 = signature age not checked.
	MaxSignatureAgeDays int `json:"max_signature_age_days"`

	RequireFirewallAllProfiles bool `json:"require_firewall_all_profiles"`

	RequirePatches bool `json:"require_patches"`
	// MaxMissingPatchAgeDays: a security or critical update missing for longer
	// than this fails the device.
	MaxMissingPatchAgeDays int `json:"max_missing_patch_age_days"`
	// MaxPatchScanAgeDays: an older (or no) scan makes the control unknown.
	MaxPatchScanAgeDays int `json:"max_patch_scan_age_days"`

	RequirePasswordPolicy bool `json:"require_password_policy"`
	// MinPasswordLength 0 = not checked.
	MinPasswordLength int `json:"min_password_length"`
	// MaxLockoutThreshold 0 = not checked. A device that never locks accounts
	// out (threshold 0) fails a check that is on.
	MaxLockoutThreshold int `json:"max_lockout_threshold"`

	RequireScreenLock    bool `json:"require_screen_lock"`
	MaxScreenLockMinutes int  `json:"max_screen_lock_minutes"`

	// MaxPostureAgeDays: posture older than this is too stale to vouch for the
	// device today, so its controls become unknown.
	MaxPostureAgeDays int `json:"max_posture_age_days"`
}

// DefaultRules is the baseline from the plan (B1).
func DefaultRules() Rules {
	return Rules{
		RequireDiskEncryption:      true,
		RequireAntivirus:           true,
		RequireRealTime:            true,
		MaxSignatureAgeDays:        7,
		RequireFirewallAllProfiles: true,
		RequirePatches:             true,
		MaxMissingPatchAgeDays:     30,
		MaxPatchScanAgeDays:        7,
		RequirePasswordPolicy:      true,
		MinPasswordLength:          8,
		MaxLockoutThreshold:        10,
		RequireScreenLock:          true,
		MaxScreenLockMinutes:       15,
		MaxPostureAgeDays:          7,
	}
}

// Validate rejects thresholds that cannot mean anything.
func (r Rules) Validate() error {
	for name, v := range map[string]int{
		"max_signature_age_days": r.MaxSignatureAgeDays, "max_missing_patch_age_days": r.MaxMissingPatchAgeDays,
		"max_patch_scan_age_days": r.MaxPatchScanAgeDays, "min_password_length": r.MinPasswordLength,
		"max_lockout_threshold": r.MaxLockoutThreshold, "max_screen_lock_minutes": r.MaxScreenLockMinutes,
		"max_posture_age_days": r.MaxPostureAgeDays,
	} {
		if v < 0 || v > 3650 {
			return fmt.Errorf("%s must be between 0 and 3650", name)
		}
	}
	if r.RequireScreenLock && r.MaxScreenLockMinutes == 0 {
		return fmt.Errorf("max_screen_lock_minutes must be set when the screen lock control is on")
	}
	if r.RequirePatches && r.MaxMissingPatchAgeDays == 0 {
		return fmt.Errorf("max_missing_patch_age_days must be set when the patch control is on")
	}
	return nil
}

// Enabled reports whether a control is part of the profile.
func (r Rules) Enabled(control string) bool {
	switch control {
	case ControlDiskEncryption:
		return r.RequireDiskEncryption
	case ControlAntivirus:
		return r.RequireAntivirus
	case ControlFirewall:
		return r.RequireFirewallAllProfiles
	case ControlPatches:
		return r.RequirePatches
	case ControlPasswordPolicy:
		return r.RequirePasswordPolicy
	case ControlScreenLock:
		return r.RequireScreenLock
	}
	return false
}

// MissingPatch is one security or critical update a device reports missing.
type MissingPatch struct {
	PatchID      string    `json:"patch_id"`
	KB           string    `json:"kb,omitempty"`
	Title        string    `json:"title"`
	Severity     string    `json:"severity"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// DeviceFacts is everything the server knows about one device that a profile
// can judge.
type DeviceFacts struct {
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname"`
	OSName       string     `json:"os_name"`
	OSVersion    string     `json:"os_version,omitempty"`
	Site         string     `json:"site,omitempty"`
	AgentVersion string     `json:"agent_version,omitempty"`
	Online       bool       `json:"online"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`

	// Posture is nil when the agent never reported one.
	Posture            *protocol.Posture `json:"-"`
	PostureCollectedAt *time.Time        `json:"posture_collected_at,omitempty"`

	// LastPatchScanAt is nil when the device was never scanned.
	LastPatchScanAt *time.Time `json:"last_patch_scan_at,omitempty"`
	// MissingSecurityPatches are missing updates of category security or
	// critical, or of severity critical, whatever their age.
	MissingSecurityPatches []MissingPatch `json:"-"`
}

// ControlResult is one control's verdict on one device.
type ControlResult struct {
	Control string `json:"control"`
	Status  string `json:"status"`
	// Reason is a short Indonesian sentence a reader of the report understands.
	Reason string `json:"reason"`
	// Evidence lists the raw values the verdict was read from.
	Evidence []string `json:"evidence,omitempty"`
	// ObservedAt is when the underlying data was collected.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// DeviceResult is a device's verdict and its controls.
type DeviceResult struct {
	Device   DeviceFacts     `json:"device"`
	Status   string          `json:"status"`
	Controls []ControlResult `json:"controls"`
}

// Control returns one control's result, if evaluated.
func (d DeviceResult) Control(c string) (ControlResult, bool) {
	for _, r := range d.Controls {
		if r.Control == c {
			return r, true
		}
	}
	return ControlResult{}, false
}

// EvaluateDevice applies rules to one device as of now.
func EvaluateDevice(rules Rules, f DeviceFacts, now time.Time) DeviceResult {
	res := DeviceResult{Device: f}
	postureGap := postureGapReason(rules, f, now)
	for _, c := range AllControls {
		if !rules.Enabled(c) {
			continue
		}
		var cr ControlResult
		if c == ControlPatches {
			cr = evalPatches(rules, f, now)
		} else if postureGap != "" {
			cr = ControlResult{Status: StatusUnknown, Reason: postureGap, ObservedAt: f.PostureCollectedAt}
		} else {
			p := f.Posture
			switch c {
			case ControlDiskEncryption:
				cr = evalDisk(p.DiskEncryption)
			case ControlAntivirus:
				cr = evalAntivirus(rules, p.Antivirus)
			case ControlFirewall:
				cr = evalFirewall(p.Firewall)
			case ControlPasswordPolicy:
				cr = evalPassword(rules, p.PasswordPolicy)
			case ControlScreenLock:
				cr = evalScreenLock(rules, p.ScreenLock)
			}
			cr.ObservedAt = f.PostureCollectedAt
		}
		cr.Control = c
		res.Controls = append(res.Controls, cr)
	}
	res.Status = deviceVerdict(res.Controls)
	return res
}

// deviceVerdict: any failure makes the device non-compliant. Otherwise any
// unknown means compliance cannot be claimed. Only a device whose every
// applicable control passed is compliant.
func deviceVerdict(cs []ControlResult) string {
	pass, unknown := 0, 0
	for _, c := range cs {
		switch c.Status {
		case StatusFail:
			return DeviceNonCompliant
		case StatusUnknown:
			unknown++
		case StatusPass:
			pass++
		}
	}
	switch {
	case unknown > 0:
		return DeviceUnknown
	case pass > 0:
		return DeviceCompliant
	default:
		return DeviceNotApplicable
	}
}

func postureGapReason(rules Rules, f DeviceFacts, now time.Time) string {
	if f.Posture == nil {
		return "Agen belum melaporkan postur keamanan (perlu versi agen yang mendukungnya)"
	}
	if f.PostureCollectedAt == nil {
		return "Waktu pengumpulan postur tidak diketahui"
	}
	if rules.MaxPostureAgeDays > 0 {
		age := daysBetween(*f.PostureCollectedAt, now)
		if age > rules.MaxPostureAgeDays {
			return fmt.Sprintf("Data postur terakhir %d hari lalu (batas %d hari); perangkat mungkin offline", age, rules.MaxPostureAgeDays)
		}
	}
	return ""
}

func daysBetween(from, to time.Time) int {
	d := to.Sub(from)
	if d < 0 {
		return 0
	}
	return int(d.Hours() / 24)
}

// passThrough handles a control the agent could not read or that does not
// exist on the device. ok is false when the caller should evaluate details.
func passThrough(state, reason, label string) (ControlResult, bool) {
	switch state {
	case protocol.PostureReported:
		return ControlResult{}, false
	case protocol.PostureNotApplicable:
		return ControlResult{Status: StatusNotApplicable, Reason: label + " tidak berlaku: " + orDash(reason)}, true
	default:
		// Includes "unknown" and any value outside the contract.
		return ControlResult{Status: StatusUnknown, Reason: label + " tidak terbaca: " + orDash(reason)}, true
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "tanpa keterangan"
	}
	return s
}

func evalDisk(p protocol.DiskEncryptionPosture) ControlResult {
	if r, done := passThrough(p.State, p.Reason, "Enkripsi disk"); done {
		return r
	}
	var ev []string
	var sys []protocol.EncryptedVolume
	for _, v := range p.Volumes {
		label := v.Mount
		if !strings.HasSuffix(label, ":") {
			label += ":"
		}
		line := label + " " + onOff(v.Protected)
		if v.Method != "" {
			line += ", " + v.Method
		}
		if v.PercentEncrypted != nil {
			line += fmt.Sprintf(", %.0f%%", *v.PercentEncrypted)
		}
		if v.Status != "" {
			line += " (" + v.Status + ")"
		}
		if v.System {
			line += " [sistem]"
			sys = append(sys, v)
		}
		ev = append(ev, line)
	}
	if len(sys) == 0 {
		return ControlResult{Status: StatusUnknown, Reason: "Volume sistem tidak ditemukan di data enkripsi", Evidence: ev}
	}
	var off []string
	for _, v := range sys {
		if !v.Protected {
			s := "BitLocker off pada " + v.Mount
			if v.Status != "" {
				s += " (" + v.Status + ")"
			}
			off = append(off, s)
		}
	}
	if len(off) > 0 {
		return ControlResult{Status: StatusFail, Reason: strings.Join(off, "; "), Evidence: ev}
	}
	return ControlResult{Status: StatusPass, Reason: "Volume sistem terenkripsi dan terlindungi", Evidence: ev}
}

func onOff(b bool) string {
	if b {
		return "On"
	}
	return "Off"
}

func triState(b *bool) string {
	switch {
	case b == nil:
		return "?"
	case *b:
		return "ya"
	default:
		return "tidak"
	}
}

func evalAntivirus(rules Rules, p protocol.AntivirusPosture) ControlResult {
	if r, done := passThrough(p.State, p.Reason, "Antivirus"); done {
		return r
	}
	var ev []string
	for _, a := range p.Products {
		line := fmt.Sprintf("%s: aktif %s, real-time %s, signature terbaru %s", a.Name,
			triState(a.Enabled), triState(a.RealTime), triState(a.UpToDate))
		if a.SignatureAgeDays != nil {
			line += fmt.Sprintf(", umur signature %d hari", *a.SignatureAgeDays)
		}
		ev = append(ev, line)
	}

	// Each active product is judged on its own; one product that passes is
	// protection. Otherwise an unknown beats a failure, because "we could not
	// tell" must not be reported as "it is off".
	var fails, unknowns []string
	activeSeen := false
	for _, a := range p.Products {
		if a.Enabled == nil {
			unknowns = append(unknowns, a.Name+": status aktif tidak diketahui")
			continue
		}
		if !*a.Enabled {
			continue
		}
		activeSeen = true
		st, why := judgeProduct(rules, a)
		switch st {
		case StatusPass:
			return ControlResult{Status: StatusPass, Reason: a.Name + " aktif" + why, Evidence: ev}
		case StatusUnknown:
			unknowns = append(unknowns, a.Name+": "+why)
		default:
			fails = append(fails, a.Name+": "+why)
		}
	}
	if len(unknowns) > 0 {
		return ControlResult{Status: StatusUnknown, Reason: strings.Join(unknowns, "; "), Evidence: ev}
	}
	if !activeSeen {
		return ControlResult{Status: StatusFail, Reason: "Tidak ada antivirus yang aktif", Evidence: ev}
	}
	return ControlResult{Status: StatusFail, Reason: strings.Join(fails, "; "), Evidence: ev}
}

// judgeProduct checks one enabled product. On pass, why is a suffix for the
// reason sentence; otherwise it explains the gap.
func judgeProduct(rules Rules, a protocol.AVProduct) (string, string) {
	if rules.RequireRealTime {
		if a.RealTime == nil {
			return StatusUnknown, "status real-time tidak diketahui"
		}
		if !*a.RealTime {
			return StatusFail, "perlindungan real-time mati"
		}
	}
	if rules.MaxSignatureAgeDays > 0 {
		switch {
		case a.SignatureAgeDays != nil:
			if *a.SignatureAgeDays > rules.MaxSignatureAgeDays {
				return StatusFail, fmt.Sprintf("signature berumur %d hari (batas %d)", *a.SignatureAgeDays, rules.MaxSignatureAgeDays)
			}
			return StatusPass, fmt.Sprintf(", signature %d hari", *a.SignatureAgeDays)
		case a.UpToDate != nil:
			// Products known only through Security Center report "up to date"
			// by their own vendor's measure, not an age in days.
			if !*a.UpToDate {
				return StatusFail, "signature tidak terbaru menurut Windows Security Center"
			}
			return StatusPass, ", signature terbaru menurut Windows Security Center"
		default:
			return StatusUnknown, "umur signature tidak diketahui"
		}
	}
	return StatusPass, ""
}

func evalFirewall(p protocol.FirewallPosture) ControlResult {
	if r, done := passThrough(p.State, p.Reason, "Firewall"); done {
		return r
	}
	if len(p.Profiles) == 0 {
		return ControlResult{Status: StatusUnknown, Reason: "Tidak ada profil firewall yang dilaporkan"}
	}
	var ev, off []string
	for _, f := range p.Profiles {
		ev = append(ev, fmt.Sprintf("%s: %s", f.Name, onOff(f.Enabled)))
		if !f.Enabled {
			off = append(off, f.Name)
		}
	}
	if len(off) > 0 {
		return ControlResult{Status: StatusFail, Reason: "Firewall off di profil " + strings.Join(off, ", "), Evidence: ev}
	}
	return ControlResult{Status: StatusPass, Reason: "Firewall aktif di semua profil", Evidence: ev}
}

func evalPassword(rules Rules, p protocol.PasswordPolicyPosture) ControlResult {
	if r, done := passThrough(p.State, p.Reason, "Kebijakan password"); done {
		return r
	}
	var ev []string
	if p.MinLength != nil {
		ev = append(ev, fmt.Sprintf("Panjang minimum: %d", *p.MinLength))
	}
	if p.MaxAgeDays != nil {
		if *p.MaxAgeDays == 0 {
			ev = append(ev, "Umur maksimum: tidak pernah kedaluwarsa")
		} else {
			ev = append(ev, fmt.Sprintf("Umur maksimum: %d hari", *p.MaxAgeDays))
		}
	}
	if p.HistoryCount != nil {
		ev = append(ev, fmt.Sprintf("Riwayat: %d password", *p.HistoryCount))
	}
	if p.LockoutThreshold != nil {
		if *p.LockoutThreshold == 0 {
			ev = append(ev, "Ambang lockout: tidak pernah terkunci")
		} else {
			ev = append(ev, fmt.Sprintf("Ambang lockout: %d percobaan", *p.LockoutThreshold))
		}
	}

	var fails, unknowns []string
	if rules.MinPasswordLength > 0 {
		switch {
		case p.MinLength == nil:
			unknowns = append(unknowns, "panjang minimum tidak terbaca")
		case *p.MinLength < rules.MinPasswordLength:
			fails = append(fails, fmt.Sprintf("panjang minimum %d (wajib ≥ %d)", *p.MinLength, rules.MinPasswordLength))
		}
	}
	if rules.MaxLockoutThreshold > 0 {
		switch {
		case p.LockoutThreshold == nil:
			unknowns = append(unknowns, "ambang lockout tidak terbaca")
		case *p.LockoutThreshold == 0:
			fails = append(fails, "akun tidak pernah terkunci setelah salah password")
		case *p.LockoutThreshold > rules.MaxLockoutThreshold:
			fails = append(fails, fmt.Sprintf("lockout setelah %d percobaan (wajib ≤ %d)", *p.LockoutThreshold, rules.MaxLockoutThreshold))
		}
	}
	switch {
	case len(fails) > 0:
		return ControlResult{Status: StatusFail, Reason: capFirst(strings.Join(fails, "; ")), Evidence: ev}
	case len(unknowns) > 0:
		return ControlResult{Status: StatusUnknown, Reason: capFirst(strings.Join(unknowns, "; ")), Evidence: ev}
	}
	return ControlResult{Status: StatusPass, Reason: "Kebijakan password memenuhi profil", Evidence: ev}
}

func evalScreenLock(rules Rules, p protocol.ScreenLockPosture) ControlResult {
	if r, done := passThrough(p.State, p.Reason, "Kunci layar"); done {
		return r
	}
	limit := rules.MaxScreenLockMinutes * 60
	if p.TimeoutSeconds == nil || *p.TimeoutSeconds <= 0 {
		return ControlResult{Status: StatusFail,
			Reason:   "Tidak ada batas inaktivitas tingkat mesin; atur lewat Kebijakan Grup (inactivity timeout)",
			Evidence: []string{"Machine inactivity limit: tidak diatur"}}
	}
	ev := []string{fmt.Sprintf("Machine inactivity limit: %d detik", *p.TimeoutSeconds)}
	if *p.TimeoutSeconds > limit {
		return ControlResult{Status: StatusFail, Evidence: ev,
			Reason: fmt.Sprintf("Layar terkunci setelah %s (wajib ≤ %d menit)", minutesText(*p.TimeoutSeconds), rules.MaxScreenLockMinutes)}
	}
	return ControlResult{Status: StatusPass, Evidence: ev,
		Reason: fmt.Sprintf("Layar terkunci setelah %s", minutesText(*p.TimeoutSeconds))}
}

func minutesText(secs int) string {
	if secs%60 == 0 {
		return fmt.Sprintf("%d menit", secs/60)
	}
	return fmt.Sprintf("%d detik", secs)
}

func evalPatches(rules Rules, f DeviceFacts, now time.Time) ControlResult {
	if f.LastPatchScanAt == nil {
		return ControlResult{Status: StatusUnknown, Reason: "Perangkat belum pernah di-scan patch"}
	}
	at := *f.LastPatchScanAt
	if rules.MaxPatchScanAgeDays > 0 {
		if age := daysBetween(at, now); age > rules.MaxPatchScanAgeDays {
			return ControlResult{Status: StatusUnknown, ObservedAt: &at,
				Reason: fmt.Sprintf("Scan patch terakhir %d hari lalu (batas %d hari)", age, rules.MaxPatchScanAgeDays)}
		}
	}
	cutoff := now.Add(-time.Duration(rules.MaxMissingPatchAgeDays) * 24 * time.Hour)
	var overdue []MissingPatch
	for _, p := range f.MissingSecurityPatches {
		if p.DiscoveredAt.Before(cutoff) {
			overdue = append(overdue, p)
		}
	}
	sort.Slice(overdue, func(i, j int) bool { return overdue[i].DiscoveredAt.Before(overdue[j].DiscoveredAt) })
	var ev []string
	for _, p := range overdue {
		id := p.KB
		if id == "" {
			id = p.PatchID
		}
		ev = append(ev, fmt.Sprintf("%s (%s) hilang sejak %s: %s", id, p.Severity, p.DiscoveredAt.Format("2006-01-02"), p.Title))
	}
	pending := len(f.MissingSecurityPatches) - len(overdue)
	if len(overdue) > 0 {
		return ControlResult{Status: StatusFail, ObservedAt: &at, Evidence: ev,
			Reason: fmt.Sprintf("%d update keamanan hilang lebih dari %d hari", len(overdue), rules.MaxMissingPatchAgeDays)}
	}
	reason := "Tidak ada update keamanan yang tertunda melewati batas"
	if pending > 0 {
		reason = fmt.Sprintf("%d update keamanan tertunda, masih dalam batas %d hari", pending, rules.MaxMissingPatchAgeDays)
	}
	return ControlResult{Status: StatusPass, ObservedAt: &at, Reason: reason}
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ControlSummary counts one control across the fleet.
type ControlSummary struct {
	Control       string `json:"control"`
	Pass          int    `json:"pass"`
	Fail          int    `json:"fail"`
	Unknown       int    `json:"unknown"`
	NotApplicable int    `json:"not_applicable"`
	// PassPercent is pass / (pass + fail); nil when nothing was evaluated.
	PassPercent *float64 `json:"pass_percent"`
}

// FleetSummary is the fleet's score. Score only counts devices that could be
// evaluated: compliant / (compliant + non-compliant). Unknown devices are
// reported next to it, never inside it.
type FleetSummary struct {
	Devices       int              `json:"devices"`
	Compliant     int              `json:"compliant"`
	NonCompliant  int              `json:"noncompliant"`
	Unknown       int              `json:"unknown"`
	NotApplicable int              `json:"not_applicable"`
	Score         *float64         `json:"score"`
	Controls      []ControlSummary `json:"controls"`
}

// Summarize builds the fleet summary for rules' enabled controls.
func Summarize(rules Rules, results []DeviceResult) FleetSummary {
	s := FleetSummary{Devices: len(results)}
	byControl := map[string]*ControlSummary{}
	for _, c := range AllControls {
		if rules.Enabled(c) {
			cs := &ControlSummary{Control: c}
			byControl[c] = cs
		}
	}
	for _, d := range results {
		switch d.Status {
		case DeviceCompliant:
			s.Compliant++
		case DeviceNonCompliant:
			s.NonCompliant++
		case DeviceNotApplicable:
			s.NotApplicable++
		default:
			s.Unknown++
		}
		for _, c := range d.Controls {
			cs := byControl[c.Control]
			if cs == nil {
				continue
			}
			switch c.Status {
			case StatusPass:
				cs.Pass++
			case StatusFail:
				cs.Fail++
			case StatusNotApplicable:
				cs.NotApplicable++
			default:
				cs.Unknown++
			}
		}
	}
	s.Score = percent(s.Compliant, s.Compliant+s.NonCompliant)
	for _, c := range AllControls {
		if cs := byControl[c]; cs != nil {
			cs.PassPercent = percent(cs.Pass, cs.Pass+cs.Fail)
			s.Controls = append(s.Controls, *cs)
		}
	}
	return s
}

func percent(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	v := float64(n) * 100 / float64(of)
	return &v
}
