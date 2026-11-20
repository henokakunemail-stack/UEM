package inventory

import "testing"

func TestChassisMappings(t *testing.T) {
	win := map[int64]string{0: "", 1: ChassisDesktop, 2: ChassisLaptop, 3: ChassisDesktop, 4: ChassisServer, 6: "", 8: ChassisLaptop, 9: ""}
	for in, want := range win {
		if got := ChassisFromPCSystemType(in); got != want {
			t.Errorf("PCSystemType %d = %q, want %q", in, got, want)
		}
	}
	smbios := map[int]string{1: "", 3: ChassisDesktop, 9: ChassisLaptop, 10: ChassisLaptop, 13: ChassisDesktop, 23: ChassisServer, 2: ""}
	for in, want := range smbios {
		if got := ChassisFromSMBIOS(in); got != want {
			t.Errorf("SMBIOS %d = %q, want %q", in, got, want)
		}
	}
	mac := map[string]string{"MacBook Pro": ChassisLaptop, "MacBook Air": ChassisLaptop, "iMac": ChassisDesktop,
		"Mac mini": ChassisDesktop, "Mac Studio": ChassisDesktop, "": "", "Apple Vision": ""}
	for in, want := range mac {
		if got := ChassisFromMacModel(in); got != want {
			t.Errorf("mac %q = %q, want %q", in, got, want)
		}
	}
}

func TestParseDMIBIOSDate(t *testing.T) {
	cases := map[string]string{"06/26/2019\n": "2019-06-26", "2019-06-26": "", "": "", "13/01/2019": ""}
	for in, want := range cases {
		if got := ParseDMIBIOSDate(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
