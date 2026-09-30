package softwaredeployment

import "testing"

// A .exe uploaded as 'msi' is the failure this guards: the agent builds
// msiexec /i <file>, which exits 1620 ("This installation package could not be
// opened") and leaves the operator with a failed task and no reason. The
// console's own form made it easy to declare the wrong type, and nothing on the
// server checked the two against each other.
func TestCheckExtensionMatches(t *testing.T) {
	cases := []struct {
		name     string
		osTarget string
		pkgType  string
		fileName string
		wantOK   bool
	}{
		{name: "exe as exe", osTarget: OSTargetWindows, pkgType: PkgTypeEXE, fileName: "winrar-x64-723.exe", wantOK: true},
		{name: "msi as msi", osTarget: OSTargetWindows, pkgType: PkgTypeMSI, fileName: "setup.msi", wantOK: true},
		{name: "msp as msi", osTarget: OSTargetWindows, pkgType: PkgTypeMSI, fileName: "patch.msp", wantOK: true},
		{name: "exe declared as msi is rejected", osTarget: OSTargetWindows, pkgType: PkgTypeMSI, fileName: "winrar-x64-723.exe", wantOK: false},
		{name: "msi declared as exe is rejected", osTarget: OSTargetWindows, pkgType: PkgTypeEXE, fileName: "setup.msi", wantOK: false},
		{name: "uppercase extension is accepted", osTarget: OSTargetWindows, pkgType: PkgTypeEXE, fileName: "WinRAR.EXE", wantOK: true},
		{name: "deb as deb", osTarget: OSTargetLinux, pkgType: "deb", fileName: "app_1.0_amd64.deb", wantOK: true},
		{name: "rpm as rpm", osTarget: OSTargetLinux, pkgType: "rpm", fileName: "app-1.0.x86_64.rpm", wantOK: true},
		{name: "deb declared as rpm is rejected", osTarget: OSTargetLinux, pkgType: "rpm", fileName: "app.deb", wantOK: false},
		{name: "pkg as pkg", osTarget: OSTargetMacOS, pkgType: "pkg", fileName: "app.pkg", wantOK: true},
		{name: "scripts are not policed on extension", osTarget: OSTargetWindows, pkgType: PkgTypeScript, fileName: "setup", wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := checkExtensionMatches(tc.osTarget, tc.pkgType, tc.fileName)
			if ok != tc.wantOK {
				t.Fatalf("checkExtensionMatches(%q, %q, %q) ok = %v, want %v (msg %q)",
					tc.osTarget, tc.pkgType, tc.fileName, ok, tc.wantOK, msg)
			}
			// A rejection has to say why, in terms the operator can act on.
			if !ok && msg == "" {
				t.Error("rejection must carry a reason")
			}
		})
	}
}

// The console only offers types that belong to the chosen OS, but the endpoint
// takes a raw form field, so the server is what actually has to hold the line.
func TestValidPackageTypesPerOS(t *testing.T) {
	cases := []struct {
		osTarget string
		pkgType  string
		wantOK   bool
	}{
		{osTarget: OSTargetWindows, pkgType: PkgTypeMSI, wantOK: true},
		{osTarget: OSTargetWindows, pkgType: PkgTypeEXE, wantOK: true},
		{osTarget: OSTargetWindows, pkgType: PkgTypeScript, wantOK: true},
		{osTarget: OSTargetWindows, pkgType: "deb", wantOK: false},
		{osTarget: OSTargetLinux, pkgType: "deb", wantOK: true},
		{osTarget: OSTargetLinux, pkgType: "rpm", wantOK: true},
		{osTarget: OSTargetLinux, pkgType: PkgTypeMSI, wantOK: false},
		{osTarget: OSTargetMacOS, pkgType: "pkg", wantOK: true},
		{osTarget: OSTargetMacOS, pkgType: PkgTypeScript, wantOK: true},
		{osTarget: OSTargetMacOS, pkgType: "rpm", wantOK: false},
		{osTarget: "solaris", pkgType: PkgTypeEXE, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.osTarget+"/"+tc.pkgType, func(t *testing.T) {
			if got := validPackageTypes[tc.osTarget][tc.pkgType]; got != tc.wantOK {
				t.Errorf("validPackageTypes[%q][%q] = %v, want %v", tc.osTarget, tc.pkgType, got, tc.wantOK)
			}
		})
	}
}
