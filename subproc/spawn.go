//go:build !windows

package subproc

import (
	"errors"
	"io"
)

// returns true if this is running as LocalSystem and SpawnSelf is necessary for desktop interaction.
func ShouldSpawn() bool {
	return false
}

// runs the currently running binary as a subprocess in the context of the active console session ID.
// cmdArgs string is appended to the exec path and passed to the subprocess.
func SpawnSelf(cmdArgs string) error {
	return errors.New("SpawnSelf not supported on non-windows platforms; ShouldSpawn always returns false and your code should use that as a guard")
}

// PersistentChild is a no-op stub on non-windows platforms.
type PersistentChild struct{}

// StartPersistentChild is unsupported off Windows.
func StartPersistentChild(cmdArgs string, displayIndex uint32, bufferSize int) (*PersistentChild, error) {
	return nil, errors.New("StartPersistentChild not supported on non-windows platforms")
}

func (*PersistentChild) LatestFrame() ([]byte, error) {
	return nil, errors.New("not supported on non-windows platforms")
}

func (*PersistentChild) UpdateDisplayIndex(displayIndex uint32) error {
	return errors.New("not supported on non-windows platforms")
}

func (*PersistentChild) Stderr() io.Reader { return nil }

func (*PersistentChild) Close() error { return nil }
