//go:build windows

package windows

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestIsARPVisible(t *testing.T) {
	tests := []struct {
		name string
		in   arpEntry
		want bool
	}{
		{
			name: "valid standard app with uninstaller",
			in: arpEntry{
				DisplayName:     "Google Chrome",
				DisplayVersion:  "120.0.6099.130",
				Publisher:       "Google LLC",
				UninstallString: `"C:\Program Files\Google\Chrome\Application\chrome.exe" --uninstall`,
			},
			want: true,
		},
		{
			name: "valid msi app with windows installer flag",
			in: arpEntry{
				DisplayName:      "7-Zip 23.01 (x64)",
				DisplayVersion:   "23.01",
				Publisher:        "Igor Pavlov",
				WindowsInstaller: true,
			},
			want: true,
		},
		{
			name: "valid app with quiet uninstaller only",
			in: arpEntry{
				DisplayName:          "Custom Enterprise Tool",
				QuietUninstallString: `"C:\Tool\uninstall.exe" /quiet`,
			},
			want: true,
		},
		{
			name: "empty display name",
			in: arpEntry{
				DisplayName:     "   ",
				UninstallString: "uninstall.exe",
			},
			want: false,
		},
		{
			name: "hidden system component",
			in: arpEntry{
				DisplayName:     "Microsoft Visual C++ 2019 X64 Additional Runtime",
				SystemComponent: true,
				UninstallString: "uninstall.exe",
			},
			want: false,
		},
		{
			name: "hidden child component with parent key name",
			in: arpEntry{
				DisplayName:     "Python 3.12 Standard Library",
				ParentKeyName:   "Python 3.12.1",
				UninstallString: "uninstall.exe",
			},
			want: false,
		},
		{
			name: "hidden child component with parent display name",
			in: arpEntry{
				DisplayName:       "Office Shared 64-bit MUI",
				ParentDisplayName: "Microsoft Office Professional Plus 2021",
				UninstallString:   "uninstall.exe",
			},
			want: false,
		},
		{
			name: "hidden driver stub without uninstaller",
			in: arpEntry{
				DisplayName: "Intel Audio Driver Helper",
			},
			want: false,
		},
		{
			name: "hidden security update release type",
			in: arpEntry{
				DisplayName:     "Security Update for Windows",
				ReleaseType:     "Security Update",
				UninstallString: "wusa.exe",
			},
			want: false,
		},
		{
			name: "hidden hotfix release type case insensitive",
			in: arpEntry{
				DisplayName:     "Hotfix for Component",
				ReleaseType:     "hotfix",
				UninstallString: "wusa.exe",
			},
			want: false,
		},
		{
			name: "hidden kb prefix update",
			in: arpEntry{
				DisplayName:     "KB5005033",
				UninstallString: "wusa.exe",
			},
			want: false,
		},
		{
			name: "hidden update for prefix",
			in: arpEntry{
				DisplayName:     "Update for Windows (KB123456)",
				UninstallString: "wusa.exe",
			},
			want: false,
		},
		{
			name: "valid app with update in name not prefix",
			in: arpEntry{
				DisplayName:     "Update Manager Professional",
				UninstallString: "uninstall.exe",
			},
			want: true,
		},
		{
			name: "valid app with kb in name not update prefix",
			in: arpEntry{
				DisplayName:     "KeePass Password Safe 2",
				UninstallString: "uninstall.exe",
			},
			want: true,
		},
		{
			name: "valid app starting with kbd but not digits",
			in: arpEntry{
				DisplayName:     "KBDJPN Input Component",
				UninstallString: "uninstall.exe",
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isARPVisible(tc.in)
			if got != tc.want {
				t.Errorf("isARPVisible(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRegIsOne(t *testing.T) {
	testKeyPath := `Software\EndpointAgentARPTest`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, testKeyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Skipf("cannot create test registry key: %v", err)
	}
	defer func() {
		k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, testKeyPath)
	}()

	// Test DWORD 1
	_ = k.SetDWordValue("DWordOne", 1)
	if !regIsOne(k, "DWordOne") {
		t.Errorf("expected DWordOne to be true")
	}

	// Test DWORD 0
	_ = k.SetDWordValue("DWordZero", 0)
	if regIsOne(k, "DWordZero") {
		t.Errorf("expected DWordZero to be false")
	}

	// Test String "1"
	_ = k.SetStringValue("StringOne", "1")
	if !regIsOne(k, "StringOne") {
		t.Errorf("expected StringOne to be true")
	}

	// Test String "0"
	_ = k.SetStringValue("StringZero", "0")
	if regIsOne(k, "StringZero") {
		t.Errorf("expected StringZero to be false")
	}

	// Test Missing
	if regIsOne(k, "Missing") {
		t.Errorf("expected Missing to be false")
	}
}

func TestLiveCollectorRunsWithoutPanic(t *testing.T) {
	c := NewCollector()
	rep, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	t.Logf("Collected %d software items on this host", len(rep.Software))
	for _, s := range rep.Software {
		if strings.TrimSpace(s.Name) == "" {
			t.Errorf("found empty software name")
		}
	}
}
