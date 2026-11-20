//go:build windows

package windows

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

func facts(t *testing.T, raw string) postureFacts {
	t.Helper()
	var f postureFacts
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Recorded from a real Windows 11 PC (non-elevated run, so BitLocker denied).
const recordedDevPC = `{"bitlocker_error":"Access denied ","defender":{"am_service":true,"antivirus":true,"real_time":false,"signature_age":0,"running_mode":"Normal"},"security_center":[{"name":"Windows Defender","state":401664}],"firewall":[{"name":"Domain","enabled":"True"},{"name":"Private","enabled":"False"},{"name":"Public","enabled":"False"}],"bios_date":"2026-06-26","users":["PC-01\\user"]}`

func TestBuildPostureFromRecordedPC(t *testing.T) {
	p := buildPosture(facts(t, recordedDevPC), nil, &passwordPolicy{MinLength: 8, MaxAgeDays: 42, History: 5, LockoutThreshold: 10}, nil, nil, nil)
	if p.DiskEncryption.State != protocol.PostureUnknown || p.DiskEncryption.Reason == "" {
		t.Errorf("denied BitLocker must be unknown with a reason, got %+v", p.DiskEncryption)
	}
	if p.Antivirus.State != protocol.PostureReported || len(p.Antivirus.Products) != 1 {
		t.Fatalf("antivirus = %+v", p.Antivirus)
	}
	av := p.Antivirus.Products[0]
	// 0x062100: snoozed (0x2000), which is not protecting.
	if av.Enabled == nil || *av.Enabled {
		t.Errorf("snoozed Defender reported as enabled")
	}
	if av.RealTime == nil || *av.RealTime || av.SignatureAgeDays == nil {
		t.Errorf("Defender detail not merged into the Security Center entry: %+v", av)
	}
	if p.Firewall.State != protocol.PostureReported || len(p.Firewall.Profiles) != 3 || !p.Firewall.Profiles[0].Enabled || p.Firewall.Profiles[2].Enabled {
		t.Errorf("firewall = %+v", p.Firewall)
	}
	if p.BIOSReleaseDate != "2026-06-26" || len(p.LoggedOnUsers) != 1 {
		t.Errorf("bios/users = %q %v", p.BIOSReleaseDate, p.LoggedOnUsers)
	}
	if *p.PasswordPolicy.MinLength != 8 || *p.PasswordPolicy.LockoutThreshold != 10 {
		t.Errorf("password policy = %+v", p.PasswordPolicy)
	}
	if p.ScreenLock.State != protocol.PostureReported || p.ScreenLock.TimeoutSeconds != nil {
		t.Errorf("no inactivity limit must be reported as not enforced, got %+v", p.ScreenLock)
	}
}

func TestAntivirusStateDecoding(t *testing.T) {
	cases := []struct {
		state    int64
		enabled  bool
		upToDate bool
	}{
		{0x061100, true, true},  // on, up to date
		{0x061110, true, false}, // on, out of date
		{0x060100, false, true}, // off
		{0x062100, false, true}, // snoozed
		{0x063100, false, true}, // expired
		{0x041000, true, true},  // third-party on
	}
	for _, c := range cases {
		f := postureFacts{SecurityCenter: []securityCenterAV{{Name: "X", State: c.state}}}
		av := buildAntivirus(f).Products[0]
		if *av.Enabled != c.enabled || *av.UpToDate != c.upToDate {
			t.Errorf("state %#x: enabled=%v upToDate=%v, want %v %v", c.state, *av.Enabled, *av.UpToDate, c.enabled, c.upToDate)
		}
	}
}

func TestBitLockerVolumes(t *testing.T) {
	f := facts(t, `{"bitlocker":[
		{"drive":"C:","volume_type":0,"protection":1,"conversion":1,"percent":100,"method":6},
		{"drive":"D:","volume_type":1,"protection":0,"conversion":0,"percent":0,"method":0},
		{"drive":"E:","volume_type":1,"protection":0,"conversion":1,"percent":100,"method":7},
		{"drive":"F:","volume_type":2,"protection":0,"conversion":0,"percent":0,"method":0},
		{"drive":"","volume_type":1,"protection":0,"conversion":0,"percent":0,"method":0}]}`)
	d := buildDiskEncryption(f)
	if d.State != protocol.PostureReported || len(d.Volumes) != 3 {
		t.Fatalf("want C:, D:, E: (removable and unmounted skipped), got %+v", d)
	}
	c, dd, e := d.Volumes[0], d.Volumes[1], d.Volumes[2]
	if !c.System || !c.Protected || c.Method != "XTS-AES 128" || c.Status != "FullyEncrypted" || *c.PercentEncrypted != 100 {
		t.Errorf("C: = %+v", c)
	}
	if dd.Protected || dd.System {
		t.Errorf("D: = %+v", dd)
	}
	if e.Protected || e.Status != "FullyEncrypted, protection suspended" {
		t.Errorf("suspended BitLocker must not count as protected: %+v", e)
	}
}

func TestBitLockerMissingNamespaceIsNotApplicable(t *testing.T) {
	d := buildDiskEncryption(postureFacts{BitLockerError: "Invalid namespace "})
	if d.State != protocol.PostureNotApplicable {
		t.Errorf("Windows Home must be not_applicable, got %+v", d)
	}
}

func TestWindowsServerWithoutSecurityCenter(t *testing.T) {
	on := true
	age := 1
	f := postureFacts{SecurityCenterError: "Invalid namespace ",
		Defender: &defenderStatus{AMService: &on, Antivirus: &on, RealTime: &on, SignatureAge: &age, RunningMode: "Normal"}}
	av := buildAntivirus(f)
	if av.State != protocol.PostureReported || len(av.Products) != 1 || !*av.Products[0].Enabled {
		t.Errorf("server with Defender = %+v", av)
	}
	f.Defender.RunningMode = "Passive Mode"
	if av := buildAntivirus(f); av.State != protocol.PostureUnknown {
		t.Errorf("passive Defender with an unseen third-party AV must be unknown, got %+v", av)
	}
}

// A script that did not run at all must not produce a single "reported".
func TestScriptFailureLeavesNothingReported(t *testing.T) {
	p := buildPosture(postureFacts{}, errors.New("powershell missing"), nil, errors.New("netapi"), nil, errors.New("reg"))
	for name, st := range map[string]string{
		"disk": p.DiskEncryption.State, "av": p.Antivirus.State, "fw": p.Firewall.State,
		"pw": p.PasswordPolicy.State, "lock": p.ScreenLock.State,
	} {
		if st != protocol.PostureUnknown {
			t.Errorf("%s = %s, want unknown", name, st)
		}
	}
}

func TestFirewallUnparseableProfileIsUnknown(t *testing.T) {
	fw := buildFirewall(postureFacts{Firewall: []firewallProfileFact{{Name: "Domain", Enabled: "NotConfigured"}}})
	if fw.State != protocol.PostureUnknown {
		t.Errorf("got %+v", fw)
	}
}
