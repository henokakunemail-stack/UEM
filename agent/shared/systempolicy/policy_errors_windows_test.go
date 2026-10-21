//go:build windows

package systempolicy

import (
	"testing"

	"golang.org/x/sys/windows"
)

// Without elevation every HKLM policy write is refused. The policy functions
// must report that; they used to return nil and the console showed the policy
// as applied. Skipped when elevated, so the test never changes the machine's
// real policy.
func TestPolicyFunctionsReportRefusedWrites(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("running elevated: these writes would succeed and change the machine's policy")
	}
	for name, apply := range map[string]func() error{
		"firewall":   func() error { return applyFirewall(false) },
		"defender":   func() error { return applyDefenderProtection(false) },
		"powershell": func() error { return applyPowerShellPolicy(false) },
		"autoplay":   func() error { return applyAutoplayPolicy(false) },
	} {
		if err := apply(); err == nil {
			t.Errorf("%s: a refused registry write was reported as success", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
}
