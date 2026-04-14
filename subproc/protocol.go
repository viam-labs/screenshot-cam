package subproc

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Wire protocol used by PersistentChild and RunChildLoop. Both sides are in the
// same binary so we only care about consistency, not stability.
//
// Request  (parent -> child): 4 bytes, little-endian uint32 display index.
// Response (child -> parent): 5 byte header (1 status byte + 4 byte LE uint32
// payload length), followed by `length` bytes of payload. Status 0 means the
// payload is JPEG bytes; non-zero means the payload is a UTF-8 error message.

const (
	statusOK  byte = 0
	statusErr byte = 1
)

func writeRequest(w io.Writer, displayIndex uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], displayIndex)
	_, err := w.Write(buf[:])
	return err
}

func readRequest(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func writeResponse(w io.Writer, status byte, payload []byte) error {
	var header [5]byte
	header[0] = status
	binary.LittleEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func readResponse(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	status := header[0]
	length := binary.LittleEndian.Uint32(header[1:])
	if length == 0 {
		return status, nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return status, payload, nil
}

// RunChildLoop reads capture requests from os.Stdin in a loop, calls capture()
// to produce JPEG bytes, and writes responses back to os.Stdout. It returns
// nil on a clean EOF (parent closed our stdin) and an error otherwise. If
// capture() returns an error the loop continues; the error is reported to the
// parent in the response and the next request is served.
func RunChildLoop(capture func(displayIndex uint32) ([]byte, error)) error {
	for {
		displayIndex, err := readRequest(os.Stdin)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading request: %w", err)
		}
		data, capErr := capture(displayIndex)
		status := statusOK
		payload := data
		if capErr != nil {
			status = statusErr
			payload = []byte(capErr.Error())
		}
		if err := writeResponse(os.Stdout, status, payload); err != nil {
			return fmt.Errorf("writing response: %w", err)
		}
	}
}
