package models

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	getActiveConsoleSessionId = kernel32.NewProc("WTSGetActiveConsoleSessionId")
)

func fixWinError(err error) error {
	// otherwise things always fail with "The operation completed successfully" sigh.
	if err == syscall.Errno(0) {
		return nil
	}
	return err
}

// on windows this calls WTSGetActiveConsoleSessionId.
// On linux it will return zero; subject to change if needed some day.
func getCurrentSession() (uint32, error) {
	var sessionID uint32
	_, _, err := getActiveConsoleSessionId.Call(uintptr(unsafe.Pointer(&sessionID)))
	return sessionID, fixWinError(err)
}

func logWindowsAndDesktops() {
	panic("next")
}