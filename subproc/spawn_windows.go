package subproc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
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
//
// Prefer StartPersistentChild for the hot path; SpawnSelf creates a fresh
// process per call and is mainly retained for the manual `-mode parent` test
// flow described in README.md.
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
	// todo: do something useful with error code from subproc. currently we're not detecting failure.
	return err
}

// PersistentChild is a long-running subprocess (started via CreateProcessAsUser
// in the active console session) that serves capture requests over stdio
// pipes. Reusing the process across calls amortizes the (substantial) cost of
// spawning a fresh Go binary for every screenshot.
type PersistentChild struct {
	process windows.Handle
	thread  windows.Handle
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File

	mu        sync.Mutex // serializes request/response on stdin/stdout
	closeOnce sync.Once
}

// makeInheritablePipe creates an anonymous pipe with both ends inheritable,
// then clears the inherit flag on the parent's end so only the child end is
// duplicated into the child by CreateProcessAsUser.
func makeInheritablePipe(childIsRead bool) (child, parent windows.Handle, err error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, &sa, 0); err != nil {
		return 0, 0, err
	}
	if childIsRead {
		child, parent = r, w
	} else {
		child, parent = w, r
	}
	if err := windows.SetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(r)
		windows.CloseHandle(w)
		return 0, 0, err
	}
	return child, parent, nil
}

// StartPersistentChild launches the current binary as a subprocess in the
// active console session with stdio plumbed back over inheritable pipes.
// cmdArgs is appended to the exec path. The returned PersistentChild can serve
// many CaptureJPEG calls; on any I/O error the caller should Close it and
// start a new one.
func StartPersistentChild(cmdArgs string) (_ *PersistentChild, retErr error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, err
	}
	token, err := activeUserToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()

	stdinChild, stdinParent, err := makeInheritablePipe(true)
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.CloseHandle(stdinParent)
		}
	}()

	stdoutChild, stdoutParent, err := makeInheritablePipe(false)
	if err != nil {
		windows.CloseHandle(stdinChild)
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.CloseHandle(stdoutParent)
		}
	}()

	stderrChild, stderrParent, err := makeInheritablePipe(false)
	if err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.CloseHandle(stderrParent)
		}
	}()

	si := new(windows.StartupInfo)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Desktop, err = syscall.UTF16PtrFromString("Winsta0\\Default")
	if err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, err
	}
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = stdinChild
	si.StdOutput = stdoutChild
	si.StdErr = stderrChild

	cmdLine, err := syscall.UTF16PtrFromString(execPath + " " + cmdArgs)
	if err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, err
	}

	pi := new(windows.ProcessInformation)
	if err := windows.CreateProcessAsUser(
		token,
		nil,
		cmdLine,
		nil,
		nil,
		true, // bInheritHandles: required so the child receives our pipe ends
		0,
		nil,
		nil,
		si,
		pi,
	); err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, fmt.Errorf("CreateProcessAsUser failed: %v", err)
	}
	// The child now owns duplicates of the child-side handles; close ours.
	windows.CloseHandle(stdinChild)
	windows.CloseHandle(stdoutChild)
	windows.CloseHandle(stderrChild)

	return &PersistentChild{
		process: pi.Process,
		thread:  pi.Thread,
		stdin:   os.NewFile(uintptr(stdinParent), "child-stdin"),
		stdout:  os.NewFile(uintptr(stdoutParent), "child-stdout"),
		stderr:  os.NewFile(uintptr(stderrParent), "child-stderr"),
	}, nil
}

// CaptureJPEG asks the child to capture the given display and returns the
// JPEG payload it writes back. Safe for concurrent callers (calls are
// serialized internally so they don't interleave on the pipes).
func (p *PersistentChild) CaptureJPEG(displayIndex uint32) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := writeRequest(p.stdin, displayIndex); err != nil {
		return nil, fmt.Errorf("writing capture request: %w", err)
	}
	status, payload, err := readResponse(p.stdout)
	if err != nil {
		return nil, fmt.Errorf("reading capture response: %w", err)
	}
	if status != statusOK {
		return nil, fmt.Errorf("child capture error: %s", string(payload))
	}
	return payload, nil
}

// Stderr returns a reader for the child's stderr stream. The caller MUST
// drain it (e.g. in a goroutine) or the child can block writing log lines
// once the OS pipe buffer fills.
func (p *PersistentChild) Stderr() io.Reader {
	return p.stderr
}

// Close terminates the child process and releases its handles. Safe to call
// multiple times.
func (p *PersistentChild) Close() error {
	p.closeOnce.Do(func() {
		// Closing stdin signals EOF to the child loop, which exits cleanly.
		_ = p.stdin.Close()
		event, _ := windows.WaitForSingleObject(p.process, 2000)
		if event != 0 { // not WAIT_OBJECT_0 -> didn't exit in time, force it
			_ = windows.TerminateProcess(p.process, 1)
		}
		_ = windows.CloseHandle(p.process)
		_ = windows.CloseHandle(p.thread)
		_ = p.stdout.Close()
		_ = p.stderr.Close()
	})
	return nil
}
