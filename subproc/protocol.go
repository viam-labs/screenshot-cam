package subproc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"
)

// Wire protocol used by PersistentChild and RunContinuousChildLoop.
//
// Config  (parent -> child on stdin, sent at startup and on reconfigure):
//
//	[4 bytes LE uint32: display index]
//
// Frames  (child -> parent on stdout, continuous stream):
//
//	[4 bytes LE uint32: payload length] [payload: JPEG bytes]

func writeConfig(w io.Writer, displayIndex uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], displayIndex)
	_, err := w.Write(buf[:])
	return err
}

func readConfig(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func writeFrame(w io.Writer, data []byte) error {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[:])
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// RunContinuousChildLoop reads an initial config from stdin, then continuously
// calls capture and writes JPEG frames to stdout. It watches stdin for config
// updates (display index changes) and EOF (shutdown signal from parent).
func RunContinuousChildLoop(capture func(displayIndex uint32, buf *bytes.Buffer) ([]byte, error)) error {
	displayIndex, err := readConfig(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading initial config: %w", err)
	}
	var currentDisplay atomic.Uint32
	currentDisplay.Store(displayIndex)

	//// Watch stdin for config updates and EOF.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			idx, err := readConfig(os.Stdin)
			if err != nil {
				return // EOF or broken pipe -> parent wants us to exit
			}
			currentDisplay.Store(idx)
		}
	}()

	// don't do this faster than ~30fps. if capture takes 80ms+ as observed, we shouldn't get here
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	var buf bytes.Buffer
	for {
		select {
		case <-done:
			return nil
		default:
		}
		data, capErr := capture(currentDisplay.Load(), &buf)
		if capErr != nil {
			fmt.Fprintf(os.Stderr, "capture error: %v\n", capErr)
			continue
		}
		if err := writeFrame(os.Stdout, data); err != nil {
			return fmt.Errorf("writing frame: %w", err)
		}
		select {
		case <-done:
			return nil
		case <-ticker.C:
		}
	}
}
