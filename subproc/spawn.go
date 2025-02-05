package subproc

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	wts32            = windows.NewLazySystemDLL("wtsapi32.dll")
	getActiveConsole = kernel32.NewProc("WTSGetActiveConsoleSessionId")
	queryToken       = wts32.NewProc("WTSQueryUserToken")
)

func activeUserToken() (windows.Token, error) {
	rawSession, _, _ := getActiveConsole.Call()
	sessionID := uint32(rawSession)
	// error value from here
	// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-wtsgetactiveconsolesessionid#return-value
	if sessionID == 0xFFFFFFFF {
		return 0, errors.New("failed to get active console")
	}
	var hToken windows.Handle
	ret, _, err := queryToken.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&hToken)))
	if ret == 0 {
		return 0, fmt.Errorf("WTSQueryUserToken error %s", err)
	}
	return windows.Token(hToken), nil
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
	si.Desktop = windows.StringToUTF16Ptr("Winsta0\\Default")

	// Setup process info
	pi := new(windows.ProcessInformation)

	// Create the process
	err = windows.CreateProcessAsUser(
		token,
		nil,
		windows.StringToUTF16Ptr(execPath+" "+cmdArgs),
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
	// todo: do something useful with error code from subproc. currently we're not detecting failure.
	return err
}
