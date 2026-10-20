//go:build windows

package remotecontrol

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")

	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalFree   = kernel32.NewProc("GlobalFree")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// openClipboard retries because the clipboard is a single lock shared by every
// application on the desktop, and clipboard managers and Office hold it for a
// few milliseconds at a time. One failed attempt is ordinary contention, not an
// error worth showing the operator.
func openClipboard() error {
	for i := 0; i < 20; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("the clipboard is held by another application")
}

// ClipboardSequence implements clipboardAccess. Windows increments it on every
// clipboard change, so comparing it is how a change is noticed without reading
// (and copying) the contents on every tick.
func (c *WindowsCapturer) ClipboardSequence() uint32 {
	n, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(n)
}

// ClipboardText implements clipboardAccess. Only text is read; an image or a
// file list on the endpoint's clipboard reads as empty rather than as an error.
func (c *WindowsCapturer) ClipboardText() (string, error) {
	// OpenClipboard binds the clipboard to the calling thread until it is
	// closed, so open, read and close must all happen on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = attachToInputDesktop()

	if err := openClipboard(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("GlobalLock on the clipboard text failed")
	}
	defer procGlobalUnlock.Call(h)
	// The handle's memory belongs to the clipboard, not to Go; reading it as a
	// NUL-terminated UTF-16 string copies it out before it is unlocked.
	text := windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&p)))
	return text, nil
}

// SetClipboardText implements clipboardAccess and returns the sequence number
// the write produced, so the caller can recognise its own change and not send
// the operator's text straight back to them.
func (c *WindowsCapturer) SetClipboardText(text string) (uint32, error) {
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return 0, fmt.Errorf("clipboard text contains a NUL: %w", err)
	}
	size := uintptr(len(u) * 2)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = attachToInputDesktop()

	h, _, callErr := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return 0, fmt.Errorf("GlobalAlloc %d bytes: %w", size, callErr)
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return 0, errors.New("GlobalLock on the new clipboard block failed")
	}
	dst := unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), len(u))
	copy(dst, u)
	procGlobalUnlock.Call(h)

	if err := openClipboard(); err != nil {
		procGlobalFree.Call(h)
		return 0, err
	}
	procEmptyClipboard.Call()
	if r, _, callErr := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		// Ownership passes to the system only on success.
		procGlobalFree.Call(h)
		procCloseClipboard.Call()
		return 0, fmt.Errorf("SetClipboardData: %w", cleanWin32Err(callErr))
	}
	// Read after closing: Windows bumps the sequence again on close, when it
	// synthesises the ANSI and OEM forms of the text, so a number read while
	// the clipboard is still open is already stale.
	procCloseClipboard.Call()
	return c.ClipboardSequence(), nil
}
