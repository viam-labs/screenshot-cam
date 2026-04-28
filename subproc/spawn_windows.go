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
	closed     atomic.Bool           // set by Close() before teardown

	closeOnce sync.Once
}

// Done returns a channel that is closed when the child process has exited
// (either crashed or was closed by the parent). Callers can select on it to
// detect child death.
func (p *PersistentChild) Done() <-chan struct{} { return p.done }

// Closed reports whether Close() has been called. Used to distinguish clean
// shutdown from unexpected child death.
func (p *PersistentChild) Closed() bool { return p.closed.Load() }

// makeInheritablePipe creates an anonymous pipe with both ends inheritable,
// then clears the inherit flag on the parent's end so only the child end is
// duplicated into the child by CreateProcessAsUser. bufferSizeHint is a hint to
// the kernel for the pipe buffer size; pass 0 to use the system default
// (~4 KB on Windows).
func makeInheritablePipe(childIsRead bool, bufferSizeHint uint32) (child, parent windows.Handle, err error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, &sa, bufferSizeHint); err != nil {
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
// The child will close when the parent terminates, since it exits on EOF from stdin or write error to stdout
// and the OS will close these when the parent process exits, whether cleanly or uncleanly.
func StartPersistentChild(cmdArgs string, displayIndex uint32, bufferSize int) (_ *PersistentChild, retErr error) {
	// Path to this same exe — we spawn a copy of ourselves with different args.
	execPath, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// Get a primary token for the active console user. Required because we run
	// as LocalSystem in session 0 but the child needs to be in the user's
	// session so it can see the desktop. Calls WTSGetActiveConsoleSessionId +
	// WTSQueryUserToken under the hood (needs SE_TCB_PRIVILEGE).
	token, err := activeUserToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()

	// Three anonymous pipes for child stdio. makeInheritablePipe creates each
	// pipe with both ends inheritable, then clears the inherit flag on the
	// parent's end so only the child end gets duplicated by CreateProcessAsUser.
	//
	// childIsRead=true for stdin (child reads from us), false for stdout/stderr
	// (child writes to us). Each defer closes the parent end ONLY if the
	// function returns an error — on success the parent end lives on in the
	// returned PersistentChild.
	stdinChild, stdinParent, err := makeInheritablePipe(true, 0)
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.CloseHandle(stdinParent)
		}
	}()

	stdoutChild, stdoutParent, err := makeInheritablePipe(false, 1<<20)
	if err != nil {
		// Manual cleanup of previously-allocated child-side handle since there's
		// no defer for it (we close it explicitly after CreateProcessAsUser).
		windows.CloseHandle(stdinChild)
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	defer func() {
		if retErr != nil {
			windows.CloseHandle(stdoutParent)
		}
	}()

	stderrChild, stderrParent, err := makeInheritablePipe(false, 0)
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

	// Build StartupInfo. Cb must be filled in with the struct size for forward
	// compat (Win32 versioning convention).
	si := new(windows.StartupInfo)
	si.Cb = uint32(unsafe.Sizeof(*si))
	// Pin the child to the interactive user desktop. Without this it would land
	// on the service desktop (Service-0x0-3e7$\Default) and BitBlt would
	// capture nothing useful. The "Winsta0\Default" string is a fixed
	// Windows-internal name, never localized.
	si.Desktop, err = syscall.UTF16PtrFromString("Winsta0\\Default")
	if err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, err
	}
	// STARTF_USESTDHANDLES makes StdInput/StdOutput/StdErr below take effect.
	// Without this flag they're ignored.
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = stdinChild
	si.StdOutput = stdoutChild
	si.StdErr = stderrChild

	// Build the command line as a wide string (Win32 expects LPWSTR).
	// Format: "<exepath> <args>". Windows parses this back into argv on the
	// child side.
	cmdLine, err := syscall.UTF16PtrFromString(execPath + " " + cmdArgs)
	if err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, err
	}

	// Spawn the child as the active console user. pi receives the child's
	// process+thread handles and PID/TID. bInheritHandles=true is what makes
	// our inheritable pipe ends actually duplicate into the child.
	pi := new(windows.ProcessInformation)
	if err := windows.CreateProcessAsUser(
		token, // user token from activeUserToken()
		nil,   // app name (nil = parse from cmdLine)
		cmdLine,
		nil, nil, // process/thread security attributes (nil = default)
		true, // bInheritHandles: required so the child receives our pipe ends
		0,    // creation flags (none — child gets default subsystem behavior)
		nil,  // environment (nil = inherit user's environment)
		nil,  // current directory (nil = inherit)
		si,
		pi,
	); err != nil {
		windows.CloseHandle(stdinChild)
		windows.CloseHandle(stdoutChild)
		windows.CloseHandle(stderrChild)
		return nil, fmt.Errorf("CreateProcessAsUser failed: %v", err)
	}
	// Close OUR copies of the child-side handles. The child now has duplicates
	// of these as its stdio handles. If we kept them open, the OS would think
	// the pipes are still alive even when the child exits — so we'd never see
	// EOF on read and shutdown detection would break.
	windows.CloseHandle(stdinChild)
	windows.CloseHandle(stdoutChild)
	windows.CloseHandle(stderrChild)

	// Wrap our parent-side stdin handle as a Go *os.File so we can use
	// Write/Close instead of raw Win32 calls.
	stdinFile := os.NewFile(uintptr(stdinParent), "child-stdin")

	// Send the first config message — 4 bytes of LE uint32 display index.
	// The child's RunContinuousChildLoop blocks at startup waiting for this
	// (see readConfig at the top of that function). The deferred handle
	// cleanups don't cover pi.Process/pi.Thread, so close them manually here.
	if err := writeConfig(stdinFile, displayIndex); err != nil {
		stdinFile.Close()
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
		return nil, fmt.Errorf("writing initial config: %w", err)
	}

	// Hand off ownership of all parent-side resources to the PersistentChild.
	// firstFrame/done are signaled by readLoop: firstFrame closes after the
	// first successful Store, done closes when readLoop exits.
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
	// Start the background frame reader. It loops on readFrame(p.stdout) and
	// stores each payload into the ring buffer.
	go pc.readLoop()

	// Wait for the first frame so callers don't get "no frame yet" from their
	// initial LatestFrame() call. Three outcomes:
	//   - firstFrame fires: child is streaming; happy path.
	//   - done fires first: child crashed before producing any output.
	//   - timeout: child is alive but stuck; tear it down and return an error.
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
		// Mark closed BEFORE teardown so supervisors watching Done() can tell
		// this was a clean shutdown rather than a crash.
		p.closed.Store(true)
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
