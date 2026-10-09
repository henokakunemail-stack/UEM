package update

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
)

// verifySwappedBinary checks that the file now in place is one the OS can
// actually run, after the swap and before the update is reported as successful.
//
// The check this replaces was os.Stat plus a non-zero size. That proves the
// rename landed and something is there, and nothing else. A truncated upload
// that still matched its recorded checksum, a text file renamed to .exe, a
// zero-filled blob: all of them pass a stat, all of them get reported to the
// server as "success", and all of them leave the fleet's version inventory
// claiming the device runs the new version while the binary on disk cannot
// start. The device then fails on its next restart, with the update recorded as
// the last thing that touched it.
//
// This is a format check, not an execution test. The agent cannot launch the
// new binary to see whether it runs -- on Windows the running agent holds the
// old image open, and the new one is a different architecture and OS often
// enough that starting it would be meaningless. So the strongest available
// claim is that the bytes are a loadable image for this platform, and that is
// what is verified.
//
// ponytail: a full smoke test would exec the new binary with a --version flag
// on a machine that can spare a process; upgrade to that if a wrong-but-
// well-formed binary ever shows up in the field.
func verifySwappedBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	header := make([]byte, 64)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	header = header[:n]
	if n < 4 {
		return errors.New("file is too short to be an executable")
	}

	if runtime.GOOS == "windows" {
		// PE: "MZ" at 0, then a little-endian offset to the PE signature at
		// 0x3c, then "PE\0\0" there.
		if n < 0x40 || header[0] != 'M' || header[1] != 'Z' {
			return errors.New("not a Windows executable: missing the MZ header")
		}
		peOffset := int64(binary.LittleEndian.Uint32(header[0x3c:0x40]))
		if peOffset < 0x40 {
			// A PE header cannot start inside the DOS header it points from.
			return errors.New("not a Windows executable: PE signature offset points into the DOS header")
		}
		if _, err := f.Seek(peOffset, 0); err != nil {
			return err
		}
		sig := make([]byte, 4)
		if _, err := io.ReadFull(f, sig); err != nil {
			return errors.New("truncated before the PE signature")
		}
		if sig[0] != 'P' || sig[1] != 'E' || sig[2] != 0 || sig[3] != 0 {
			return errors.New("not a Windows executable: missing the PE signature")
		}
		return nil
	}

	if runtime.GOOS == "darwin" {
		if n < 4 {
			return errors.New("not a macOS executable: missing the Mach-O header")
		}
		magic := binary.BigEndian.Uint32(header[:4])
		magicLE := binary.LittleEndian.Uint32(header[:4])
		if magic == 0xfeedface || magic == 0xfeedfacf || magic == 0xcafebabe || magic == 0xcafebabf ||
			magicLE == 0xfeedface || magicLE == 0xfeedfacf || magicLE == 0xcafebabe || magicLE == 0xcafebabf {
			return nil
		}
		return errors.New("not a macOS executable: missing the Mach-O header")
	}

	// ELF: "\x7fELF".
	if n < 4 || header[0] != 0x7f || header[1] != 'E' || header[2] != 'L' || header[3] != 'F' {
		return errors.New("not an ELF executable: missing the ELF header")
	}
	return nil
}
