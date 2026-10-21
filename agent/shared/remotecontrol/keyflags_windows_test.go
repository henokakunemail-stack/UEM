//go:build windows

package remotecontrol

import "testing"

// 0xBA is the ';:' key on a US layout. Marked extended, it was sent as E0 27
// and typed nothing. NumLock is extended and was missing.
func TestExtendedKeyTable(t *testing.T) {
	cases := map[uint16]bool{
		0xBA: false, // ;:
		0x41: false, // A
		0x0D: false, // main Enter
		0x25: true,  // Left arrow
		0x2E: true,  // Delete
		0x90: true,  // NumLock
		0xA3: true,  // RCtrl
		0xA2: false, // LCtrl
		0xAD: true,  // Volume mute
	}
	for vk, want := range cases {
		if got := isExtendedVK(vk); got != want {
			t.Errorf("isExtendedVK(0x%X) = %v, want %v", vk, got, want)
		}
	}
	layout, _, _ := procGetKeyboardLayout.Call(0)
	if _, flags := keyFlags(0x0D, true, layout); flags&KEYEVENTF_EXTENDEDKEY == 0 {
		t.Error("numpad Enter (location 3) is not sent as an extended key")
	}
	if _, flags := keyFlags(0x0D, false, layout); flags&KEYEVENTF_EXTENDEDKEY != 0 {
		t.Error("main Enter is sent as an extended key")
	}
}
