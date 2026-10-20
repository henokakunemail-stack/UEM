package x11

// The console sends the browser's legacy keyCode, which is the Windows
// virtual-key code. X needs a keycode, and the way there is through the keysym
// the key produces unshifted: VK_A is the "a" key, whatever the layout puts on
// it with Shift. Shift itself arrives as its own key event, so the X server
// applies it exactly as it would for a local keyboard.
var vkKeysym = map[int]uint32{
	0x08: 0xff08, // BackSpace
	0x09: 0xff09, // Tab
	0x0C: 0xff0b, // Clear
	0x0D: 0xff0d, // Return
	0x10: 0xffe1, // Shift_L
	0x11: 0xffe3, // Control_L
	0x12: 0xffe9, // Alt_L
	0x13: 0xff13, // Pause
	0x14: 0xffe5, // Caps_Lock
	0x1B: 0xff1b, // Escape
	0x20: 0x0020, // space
	0x21: 0xff55, // Prior (Page Up)
	0x22: 0xff56, // Next (Page Down)
	0x23: 0xff57, // End
	0x24: 0xff50, // Home
	0x25: 0xff51, // Left
	0x26: 0xff52, // Up
	0x27: 0xff53, // Right
	0x28: 0xff54, // Down
	0x2C: 0xff61, // Print
	0x2D: 0xff63, // Insert
	0x2E: 0xffff, // Delete
	0x5B: 0xffeb, // Super_L (Windows key)
	0x5C: 0xffec, // Super_R
	0x5D: 0xff67, // Menu
	0x6A: 0xffaa, // KP_Multiply
	0x6B: 0xffab, // KP_Add
	0x6C: 0xffac, // KP_Separator
	0x6D: 0xffad, // KP_Subtract
	0x6E: 0xffae, // KP_Decimal
	0x6F: 0xffaf, // KP_Divide
	0x90: 0xff7f, // Num_Lock
	0x91: 0xff14, // Scroll_Lock
	0xA0: 0xffe1, // Shift_L
	0xA1: 0xffe2, // Shift_R
	0xA2: 0xffe3, // Control_L
	0xA3: 0xffe4, // Control_R
	0xA4: 0xffe9, // Alt_L
	0xA5: 0xffea, // Alt_R
	0xBA: 0x003b, // ;
	0xBB: 0x003d, // =
	0xBC: 0x002c, // ,
	0xBD: 0x002d, // -
	0xBE: 0x002e, // .
	0xBF: 0x002f, // /
	0xC0: 0x0060, // `
	0xDB: 0x005b, // [
	0xDC: 0x005c, // backslash
	0xDD: 0x005d, // ]
	0xDE: 0x0027, // '
	0xE2: 0x003c, // < on the ISO key left of Z
}

// Fallbacks for keysyms a layout may not carry: AltGr layouts often map the
// right Alt key to ISO_Level3_Shift instead of Alt_R.
var keysymFallback = map[uint32]uint32{
	0xffea: 0xfe03, // Alt_R -> ISO_Level3_Shift
	0xffec: 0xffeb, // Super_R -> Super_L
}

// VKToKeysym maps a Windows virtual-key code to an X keysym, or 0.
func VKToKeysym(vk int) uint32 {
	switch {
	case vk >= 0x30 && vk <= 0x39: // 0-9
		return uint32(vk)
	case vk >= 0x41 && vk <= 0x5A: // A-Z -> a-z
		return uint32(vk + 0x20)
	case vk >= 0x60 && vk <= 0x69: // numpad 0-9
		return 0xffb0 + uint32(vk-0x60)
	case vk >= 0x70 && vk <= 0x87: // F1-F24
		return 0xffbe + uint32(vk-0x70)
	}
	return vkKeysym[vk]
}

// Keymap finds the keycode that produces a keysym.
type Keymap struct {
	min     byte
	perCode int
	syms    []uint32
}

// NewKeymap wraps a GetKeyboardMapping answer.
func NewKeymap(min byte, perCode int, syms []uint32) *Keymap {
	return &Keymap{min: min, perCode: perCode, syms: syms}
}

// Keycode returns the keycode for a keysym, preferring one where it is the
// unshifted symbol, then any column, then a fallback keysym. 0 means none.
func (k *Keymap) Keycode(sym uint32) byte {
	if k == nil || k.perCode == 0 || sym == 0 {
		return 0
	}
	if c := k.find(sym); c != 0 {
		return c
	}
	if alt, ok := keysymFallback[sym]; ok {
		return k.find(alt)
	}
	return 0
}

func (k *Keymap) find(sym uint32) byte {
	n := len(k.syms) / k.perCode
	for col := 0; col < k.perCode; col++ {
		for i := 0; i < n; i++ {
			if k.syms[i*k.perCode+col] == sym {
				return k.min + byte(i)
			}
		}
	}
	return 0
}
