package software

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// msiexec writes UTF-16LE, so the raw bytes of its diagnostics reach the agent
// with a NUL between every character. The console's View Logs panel renders that
// verbatim, which is how a failed install showed up as an unreadable run of
// blanks with the real reason ("This installation package could not be
// opened") invisible inside it.
func TestDecodeOutput(t *testing.T) {
	const msg = "This installation package could not be opened."

	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "utf16le without bom",
			in:   utf16le(msg),
			want: msg,
		},
		{
			name: "utf16le with bom",
			in:   append([]byte{0xFF, 0xFE}, utf16le(msg)...),
			want: msg,
		},
		{
			name: "utf16be with bom",
			in:   append([]byte{0xFE, 0xFF}, utf16be(msg)...),
			want: msg,
		},
		{
			name: "utf8 passes through",
			in:   []byte("Successfully installed.\n"),
			want: "Successfully installed.\n",
		},
		{
			name: "utf8 containing a nul is left alone",
			in:   []byte{'a', 0x00, 'b'},
			want: "a\x00b",
		},
		{
			name: "empty input",
			in:   nil,
			want: "",
		},
		{
			name: "trailing nul terminator is dropped",
			in:   append(utf16le("ok"), 0x00, 0x00),
			want: "ok",
		},
		{
			name: "non ascii decodes when a bom is present",
			in:   append([]byte{0xFF, 0xFE}, utf16le("安装成功 — selesai")...),
			want: "安装成功 — selesai",
		},
		{
			name: "unpaired surrogate is replaced, not dropped",
			// 0x00,0xD8 little-endian is U+D800, a high surrogate with no pair.
			in:   []byte{0xFF, 0xFE, 0x00, 0xD8, 'o', 0x00, 'k', 0x00},
			want: "�ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeOutput(tc.in)
			if got != tc.want {
				t.Errorf("decodeOutput() = %q, want %q", got, tc.want)
			}
			// The one case that keeps its NULs is the one that must: a pass-through
			// is allowed to be binary.
			if !strings.ContainsRune(tc.want, 0) && strings.ContainsRune(got, 0) {
				t.Errorf("decodeOutput() left a NUL in the output: %q", got)
			}
		})
	}
}

func utf16le(s string) []byte {
	return encodeUTF16(s, false)
}

func utf16be(s string) []byte {
	return encodeUTF16(s, true)
}

func encodeUTF16(s string, bigEndian bool) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		if bigEndian {
			out = append(out, byte(c>>8), byte(c))
		} else {
			out = append(out, byte(c), byte(c>>8))
		}
	}
	return out
}

// An .exe with no silent switches used to be launched bare, which pops a GUI
// on the endpoint's desktop and never returns. The runner must refuse instead.
func TestWindowsRunnerRefusesBareExe(t *testing.T) {
	_, _, err := (&windowsRunner{}).Run(t.Context(), "C:\\tmp\\setup.exe", "exe", "")
	if err == nil {
		t.Fatal("expected an error for an .exe with no silent-install arguments")
	}
	if !strings.Contains(err.Error(), "silent") {
		t.Errorf("error should name the missing silent arguments, got: %v", err)
	}
}
