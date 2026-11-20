package compliance

import (
	"strings"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

var testNow = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

func bp(b bool) *bool { return &b }
func ip(i int) *int   { return &i }
func tp(t time.Time) *time.Time {
	return &t
}

// goodPosture passes every control of DefaultRules.
func goodPosture() *protocol.Posture {
	return &protocol.Posture{
		SchemaVersion: 1,
		DiskEncryption: protocol.DiskEncryptionPosture{State: "reported", Volumes: []protocol.EncryptedVolume{
			{Mount: "C:", System: true, Protected: true, Method: "XTS-AES 128", Status: "FullyEncrypted"},
			{Mount: "D:", Protected: false}}},
		Antivirus: protocol.AntivirusPosture{State: "reported", Products: []protocol.AVProduct{
			{Name: "Windows Defender", Enabled: bp(true), RealTime: bp(true), SignatureAgeDays: ip(1)}}},
		Firewall: protocol.FirewallPosture{State: "reported", Profiles: []protocol.FirewallProfile{
			{Name: "Domain", Enabled: true}, {Name: "Private", Enabled: true}, {Name: "Public", Enabled: true}}},
		PasswordPolicy: protocol.PasswordPolicyPosture{State: "reported", MinLength: ip(8), MaxAgeDays: ip(90), LockoutThreshold: ip(5)},
		ScreenLock:     protocol.ScreenLockPosture{State: "reported", TimeoutSeconds: ip(600), RequirePassword: bp(true)},
	}
}

func goodFacts(id string) DeviceFacts {
	return DeviceFacts{ID: id, Hostname: id, OSName: "windows", Online: true,
		Posture: goodPosture(), PostureCollectedAt: tp(testNow.Add(-2 * time.Hour)),
		LastPatchScanAt: tp(testNow.Add(-5 * time.Hour))}
}

func control(t *testing.T, r DeviceResult, c string) ControlResult {
	t.Helper()
	cr, ok := r.Control(c)
	if !ok {
		t.Fatalf("control %s not evaluated", c)
	}
	return cr
}

// The plan's acceptance case: compliant, BitLocker off, old agent -> one of
// each, and a fleet score of 50% (1 of the 2 that could be evaluated), not
// 33% (unknown counted as failing) or 66% (unknown counted as passing).
func TestFleetOfThreeScoresOnlyEvaluatedDevices(t *testing.T) {
	rules := DefaultRules()
	ok := goodFacts("PC-OK")

	off := goodFacts("PC-BITLOCKER-OFF")
	off.Posture.DiskEncryption.Volumes[0].Protected = false
	off.Posture.DiskEncryption.Volumes[0].Status = ""

	old := goodFacts("PC-OLD-AGENT")
	old.Posture, old.PostureCollectedAt = nil, nil

	var results []DeviceResult
	for _, f := range []DeviceFacts{ok, off, old} {
		results = append(results, EvaluateDevice(rules, f, testNow))
	}
	if results[0].Status != DeviceCompliant {
		t.Fatalf("PC-OK: %s %+v", results[0].Status, results[0].Controls)
	}
	if results[1].Status != DeviceNonCompliant {
		t.Fatalf("PC-BITLOCKER-OFF: %s", results[1].Status)
	}
	if got := control(t, results[1], ControlDiskEncryption).Reason; got != "BitLocker off pada C:" {
		t.Fatalf("reason = %q", got)
	}
	if results[2].Status != DeviceUnknown {
		t.Fatalf("PC-OLD-AGENT: %s", results[2].Status)
	}
	// The old agent still has patch data; only its posture controls are unknown.
	if control(t, results[2], ControlPatches).Status != StatusPass {
		t.Fatal("old agent's patch control should still be judged")
	}
	if !strings.Contains(control(t, results[2], ControlFirewall).Reason, "belum melaporkan postur") {
		t.Fatalf("old agent reason: %q", control(t, results[2], ControlFirewall).Reason)
	}

	s := Summarize(rules, results)
	if s.Compliant != 1 || s.NonCompliant != 1 || s.Unknown != 1 || s.Devices != 3 {
		t.Fatalf("summary %+v", s)
	}
	if s.Score == nil || *s.Score != 50 {
		t.Fatalf("score = %v, want 50", s.Score)
	}
	for _, c := range s.Controls {
		if c.Control == ControlDiskEncryption && (c.Pass != 1 || c.Fail != 1 || c.Unknown != 1 || *c.PassPercent != 50) {
			t.Fatalf("disk control summary %+v", c)
		}
	}
}

func TestNoEvaluatedDeviceHasNoScore(t *testing.T) {
	f := goodFacts("x")
	f.Posture = nil
	s := Summarize(DefaultRules(), []DeviceResult{EvaluateDevice(DefaultRules(), f, testNow)})
	if s.Score != nil {
		t.Fatalf("a fleet with nothing evaluated must have no score, got %v", *s.Score)
	}
}

func TestStalePostureIsUnknown(t *testing.T) {
	f := goodFacts("x")
	f.PostureCollectedAt = tp(testNow.Add(-9 * 24 * time.Hour))
	r := EvaluateDevice(DefaultRules(), f, testNow)
	if r.Status != DeviceUnknown {
		t.Fatalf("9-day-old posture: %s", r.Status)
	}
	if !strings.Contains(control(t, r, ControlAntivirus).Reason, "9 hari") {
		t.Fatalf("reason %q", control(t, r, ControlAntivirus).Reason)
	}
}

func TestPassThroughStates(t *testing.T) {
	f := goodFacts("x")
	f.Posture.DiskEncryption = protocol.DiskEncryptionPosture{State: "not_applicable", Reason: "Windows Home"}
	f.Posture.Firewall = protocol.FirewallPosture{State: "compliant"} // outside the contract
	r := EvaluateDevice(DefaultRules(), f, testNow)
	if c := control(t, r, ControlDiskEncryption); c.Status != StatusNotApplicable {
		t.Fatalf("disk: %+v", c)
	}
	if c := control(t, r, ControlFirewall); c.Status != StatusUnknown {
		t.Fatalf("an unrecognised state must be unknown, got %+v", c)
	}
}

func TestDiskWithoutSystemVolumeIsUnknown(t *testing.T) {
	f := goodFacts("x")
	f.Posture.DiskEncryption.Volumes = []protocol.EncryptedVolume{{Mount: "D:", Protected: true}}
	if c := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlDiskEncryption); c.Status != StatusUnknown {
		t.Fatalf("got %+v", c)
	}
}

func TestAntivirus(t *testing.T) {
	cases := []struct {
		name     string
		products []protocol.AVProduct
		want     string
		reason   string
	}{
		{"none", nil, StatusFail, "Tidak ada antivirus yang aktif"},
		{"only disabled", []protocol.AVProduct{{Name: "A", Enabled: bp(false)}}, StatusFail, "Tidak ada antivirus"},
		{"real-time off", []protocol.AVProduct{{Name: "A", Enabled: bp(true), RealTime: bp(false), SignatureAgeDays: ip(0)}}, StatusFail, "real-time mati"},
		{"old signature", []protocol.AVProduct{{Name: "A", Enabled: bp(true), RealTime: bp(true), SignatureAgeDays: ip(12)}}, StatusFail, "12 hari"},
		{"security center up to date", []protocol.AVProduct{{Name: "ESET", Enabled: bp(true), RealTime: bp(true), UpToDate: bp(true)}}, StatusPass, "ESET"},
		{"security center out of date", []protocol.AVProduct{{Name: "ESET", Enabled: bp(true), RealTime: bp(true), UpToDate: bp(false)}}, StatusFail, "tidak terbaru"},
		{"signature unknown", []protocol.AVProduct{{Name: "A", Enabled: bp(true), RealTime: bp(true)}}, StatusUnknown, "tidak diketahui"},
		{"real-time unknown", []protocol.AVProduct{{Name: "A", Enabled: bp(true), SignatureAgeDays: ip(1)}}, StatusUnknown, "real-time"},
		{"one good product is enough", []protocol.AVProduct{
			{Name: "Old", Enabled: bp(true), RealTime: bp(false)},
			{Name: "Defender", Enabled: bp(true), RealTime: bp(true), SignatureAgeDays: ip(2)}}, StatusPass, "Defender"},
		{"unknown beats fail", []protocol.AVProduct{
			{Name: "Old", Enabled: bp(true), RealTime: bp(false)},
			{Name: "Mystery", Enabled: nil}}, StatusUnknown, "Mystery"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := goodFacts("x")
			f.Posture.Antivirus.Products = c.products
			got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlAntivirus)
			if got.Status != c.want || !strings.Contains(got.Reason, c.reason) {
				t.Fatalf("got %s %q, want %s containing %q", got.Status, got.Reason, c.want, c.reason)
			}
		})
	}
}

func TestFirewall(t *testing.T) {
	f := goodFacts("x")
	f.Posture.Firewall.Profiles[2].Enabled = false
	got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlFirewall)
	if got.Status != StatusFail || got.Reason != "Firewall off di profil Public" {
		t.Fatalf("got %+v", got)
	}
	f.Posture.Firewall.Profiles = nil
	if got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlFirewall); got.Status != StatusUnknown {
		t.Fatalf("no profiles: %+v", got)
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := []struct {
		name    string
		min     *int
		lockout *int
		want    string
		reason  string
	}{
		{"meets", ip(8), ip(10), StatusPass, ""},
		{"short", ip(6), ip(5), StatusFail, "Panjang minimum 6"},
		{"never locks", ip(8), ip(0), StatusFail, "tidak pernah terkunci"},
		{"lockout too lax", ip(8), ip(50), StatusFail, "50 percobaan"},
		{"unreadable", nil, ip(5), StatusUnknown, "tidak terbaca"},
		{"fail beats unknown", ip(4), nil, StatusFail, "Panjang minimum 4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := goodFacts("x")
			f.Posture.PasswordPolicy.MinLength, f.Posture.PasswordPolicy.LockoutThreshold = c.min, c.lockout
			got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlPasswordPolicy)
			if got.Status != c.want || !strings.Contains(got.Reason, c.reason) {
				t.Fatalf("got %s %q", got.Status, got.Reason)
			}
		})
	}
}

func TestScreenLock(t *testing.T) {
	f := goodFacts("x")
	f.Posture.ScreenLock = protocol.ScreenLockPosture{State: "reported"}
	got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlScreenLock)
	if got.Status != StatusFail || !strings.Contains(got.Reason, "Kebijakan Grup") {
		t.Fatalf("no limit: %+v", got)
	}
	f.Posture.ScreenLock.TimeoutSeconds = ip(1800)
	if got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlScreenLock); got.Status != StatusFail {
		t.Fatalf("30 minutes over a 15-minute limit: %+v", got)
	}
	f.Posture.ScreenLock.TimeoutSeconds = ip(900)
	if got := control(t, EvaluateDevice(DefaultRules(), f, testNow), ControlScreenLock); got.Status != StatusPass {
		t.Fatalf("exactly 15 minutes: %+v", got)
	}
}

func TestPatches(t *testing.T) {
	rules := DefaultRules()
	f := goodFacts("x")
	f.MissingSecurityPatches = []MissingPatch{
		{PatchID: "a", KB: "KB5001", Severity: "critical", Title: "Old", DiscoveredAt: testNow.Add(-45 * 24 * time.Hour)},
		{PatchID: "b", KB: "KB5002", Severity: "important", Title: "New", DiscoveredAt: testNow.Add(-3 * 24 * time.Hour)},
	}
	got := control(t, EvaluateDevice(rules, f, testNow), ControlPatches)
	if got.Status != StatusFail || !strings.Contains(got.Reason, "1 update keamanan") || len(got.Evidence) != 1 ||
		!strings.Contains(got.Evidence[0], "KB5001") {
		t.Fatalf("overdue: %+v", got)
	}

	f.MissingSecurityPatches = f.MissingSecurityPatches[1:]
	got = control(t, EvaluateDevice(rules, f, testNow), ControlPatches)
	if got.Status != StatusPass || !strings.Contains(got.Reason, "1 update keamanan tertunda") {
		t.Fatalf("within limit: %+v", got)
	}

	f.LastPatchScanAt = tp(testNow.Add(-10 * 24 * time.Hour))
	if got := control(t, EvaluateDevice(rules, f, testNow), ControlPatches); got.Status != StatusUnknown {
		t.Fatalf("stale scan: %+v", got)
	}
	f.LastPatchScanAt = nil
	if got := control(t, EvaluateDevice(rules, f, testNow), ControlPatches); got.Status != StatusUnknown {
		t.Fatalf("never scanned: %+v", got)
	}
}

func TestDisabledControlsAreNotEvaluated(t *testing.T) {
	rules := DefaultRules()
	rules.RequireScreenLock = false
	f := goodFacts("x")
	f.Posture.ScreenLock = protocol.ScreenLockPosture{State: "reported"} // would fail
	r := EvaluateDevice(rules, f, testNow)
	if _, ok := r.Control(ControlScreenLock); ok || r.Status != DeviceCompliant {
		t.Fatalf("disabled control leaked into the verdict: %+v", r)
	}
	s := Summarize(rules, []DeviceResult{r})
	if len(s.Controls) != 5 {
		t.Fatalf("summary should list the 5 enabled controls, got %d", len(s.Controls))
	}
}

func TestRulesValidate(t *testing.T) {
	if err := DefaultRules().Validate(); err != nil {
		t.Fatal(err)
	}
	r := DefaultRules()
	r.MinPasswordLength = -1
	if r.Validate() == nil {
		t.Fatal("negative threshold accepted")
	}
	r = DefaultRules()
	r.MaxScreenLockMinutes = 0
	if r.Validate() == nil {
		t.Fatal("screen lock on with no limit accepted")
	}
}

func TestPreviousPeriod(t *testing.T) {
	// 1 Dec 00:30 WIB is still 30 Nov in UTC; the report period follows WIB.
	if got := PreviousPeriod(time.Date(2026, 11, 30, 17, 30, 0, 0, time.UTC)); got != "2026-11" {
		t.Fatalf("got %s", got)
	}
	if got := PreviousPeriod(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); got != "2025-12" {
		t.Fatalf("got %s", got)
	}
}
