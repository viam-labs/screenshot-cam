package subproc

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	wts32            = windows.NewLazySystemDLL("wtsapi32.dll")
	getActiveConsole = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	queryToken       = wts32.NewProc("WTSQueryUserToken")

	// ErrNoDesktopSession is returned when there is no interactive desktop session available for screenshotting.
	ErrNoDesktopSession = errors.New("no interactive desktop session available (is a user logged in at the console?)")
)

// HasDesktopSession returns true if there is an active console session with a valid user token.
func HasDesktopSession() bool {
	rawSession, _, _ := getActiveConsole.Call()
	sessionID := uint32(rawSession)
	if sessionID == 0xFFFFFFFF {
		return false
	}
	var hToken windows.Handle
	ret, _, _ := queryToken.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&hToken)))
	if ret == 0 {
		return false
	}
	windows.CloseHandle(hToken)
	return true
}

func activeUserToken() (windows.Token, error) {
	rawSession, _, _ := getActiveConsole.Call()
	sessionID := uint32(rawSession)
	// error value from here
	// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-wtsgetactiveconsolesessionid#return-value
	if sessionID == 0xFFFFFFFF {
		return 0, ErrNoDesktopSession
	}
	var hToken windows.Handle
	ret, _, err := queryToken.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&hToken)))
	if ret == 0 {
		return 0, fmt.Errorf("WTSQueryUserToken for session %d: %s", sessionID, err)
	}
	return windows.Token(hToken), nil
}

// returns true if this is running as session 0 and SpawnSelf is necessary for desktop interaction.
func ShouldSpawn() bool {
	pid := windows.GetCurrentProcessId()
	var sid uint32
	if err := windows.ProcessIdToSessionId(pid, &sid); err != nil {
		return false
	}
	return sid == 0
}

// runs the currently running binary as a subprocess in the context of the active console session ID.
// cmdArgs string is appended to the exec path and passed to the subprocess.
func SpawnSelf(cmdArgs string) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	token, err := activeUserToken()
	if err != nil {
		return err
	}
	defer token.Close()
	// todo: why do some docs recommend this here?
	// var token windows.Token
	// err = windows.DuplicateTokenEx(origToken, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &token)
	// if err != nil {
	// 	return fmt.Errorf("Failed to duplicate token: %v", err)
	// }
	// defer token.Close()

	si := new(windows.StartupInfo)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Desktop, err = syscall.UTF16PtrFromString("Winsta0\\Default")
	if err != nil {
		return err
	}
	cmdLine, err := syscall.UTF16PtrFromString(execPath + " " + cmdArgs)
	if err != nil {
		return err
	}

	// Setup process info
	pi := new(windows.ProcessInformation)

	// Create the process
	err = windows.CreateProcessAsUser(
		token,
		nil,
		cmdLine,
		nil,
		nil,
		false,
		0,
		nil,
		nil,
		si,
		pi,
	)
	if err != nil {
		return fmt.Errorf("CreateProcessAsUser failed: %v", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	_, err = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("WaitForSingleObject failed: %v", err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(pi.Process, &exitCode); err != nil {
		return fmt.Errorf("GetExitCodeProcess failed: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("child process exited with code %d", exitCode)
	}
	return nil
}
