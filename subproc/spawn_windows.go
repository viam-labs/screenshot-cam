package subproc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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
	// todo: do something useful with error code from subproc. currently we're not detecting failure.
	return err
}

// PersistentChild is a long-running subprocess (started via CreateProcessAsUser
// in the active console session) that continuously captures screenshots and
// streams them back over a stdout pipe. The parent reads frames into a
// RingBuffer and serves them to callers via LatestFrame.
type PersistentChild struct {
	process windows.Handle
	thread  windows.Handle
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File

	buf        *RingBuffer
	readerErr  atomic.Pointer[error] // set by readLoop on fatal pipe error
	firstFrame chan struct{}         // closed after the first Store
	done       chan struct{}         // closed when readLoop exits

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
// cmdArgs is appended to the exec path. The child immediately begins capturing
// the given display and streaming frames. The returned PersistentChild serves
// LatestFrame from a lock-free ring buffer; on any fatal error the caller
// should Close it and start a new one.
func StartPersistentChild(cmdArgs string, displayIndex uint32, bufferSize int) (_ *PersistentChild, retErr error) {
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

	stdinFile := os.NewFile(uintptr(stdinParent), "child-stdin")

	// Send initial config (display index) to the child.
	if err := writeConfig(stdinFile, displayIndex); err != nil {
		stdinFile.Close()
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
		return nil, fmt.Errorf("writing initial config: %w", err)
	}

	pc := &PersistentChild{
		process:    pi.Process,
		thread:     pi.Thread,
		stdin:      stdinFile,
		stdout:     os.NewFile(uintptr(stdoutParent), "child-stdout"),
		stderr:     os.NewFile(uintptr(stderrParent), "child-stderr"),
		buf:        NewRingBuffer(bufferSize),
		firstFrame: make(chan struct{}),
		done:       make(chan struct{}),
	}
	go pc.readLoop()

	// Wait for the first frame so callers don't get "no frame yet" immediately.
	select {
	case <-pc.firstFrame:
	case <-pc.done:
		// readLoop exited before delivering a frame.
		if ep := pc.readerErr.Load(); ep != nil {
			return nil, fmt.Errorf("child exited before first frame: %w", *ep)
		}
		return nil, errors.New("child exited before first frame")
	case <-time.After(10 * time.Second):
		pc.Close()
		return nil, errors.New("timed out waiting for first frame from child")
	}

	return pc, nil
}

// readLoop reads frames from the child's stdout pipe and stores them in the
// ring buffer. It runs until the pipe is closed or an error occurs.
func (p *PersistentChild) readLoop() {
	defer close(p.done)
	first := true
	for {
		start := time.Now()
		data, err := readFrame(p.stdout)
		if err != nil {
			p.readerErr.Store(&err)
			return
		}
		p.buf.Store(data)
		done := time.Now()
		if done.Sub(start) > 800*time.Millisecond {
			fmt.Fprintf(os.Stderr, "getting latest screenshot from child at %v took %v\n", done, done.Sub(start))
		}
		if first {
			close(p.firstFrame)
			first = false
		}
	}
}

// LatestFrame returns the most recently captured screenshot as JPEG bytes.
// It never blocks on pipe I/O; the data comes from the lock-free ring buffer.
func (p *PersistentChild) LatestFrame() ([]byte, error) {
	if ep := p.readerErr.Load(); ep != nil {
		return nil, *ep
	}
	data := p.buf.Load()
	if data == nil {
		return nil, errors.New("no frame available yet")
	}
	return data, nil
}

// UpdateDisplayIndex sends a new display index to the child. The child's
// capture loop picks it up atomically on the next iteration.
func (p *PersistentChild) UpdateDisplayIndex(displayIndex uint32) error {
	return writeConfig(p.stdin, displayIndex)
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
		// Closing stdin signals EOF to the child's config-reading goroutine,
		// which causes the capture loop to exit.
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
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
