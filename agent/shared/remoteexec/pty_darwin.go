//go:build darwin

package remoteexec

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal pair the way posix_openpt, grantpt,
// unlockpt and ptsname do on macOS, with the same ioctls libc issues, so the
// agent stays CGO-free.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := uintptr(master.Fd())
	fail := func(step string, e error) (*os.File, *os.File, error) {
		_ = master.Close()
		return nil, nil, fmt.Errorf("%s: %w", step, e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGRANT, 0); e != 0 {
		return fail("grant pty", e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYUNLK, 0); e != 0 {
		return fail("unlock pty", e)
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		return fail("pty name", e)
	}
	end := bytes.IndexByte(name[:], 0)
	if end <= 0 {
		return fail("pty name", fmt.Errorf("kernel returned no slave name"))
	}
	path := string(name[:end])
	slave, err = os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
