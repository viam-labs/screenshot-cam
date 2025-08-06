//go:build !windows

package subproc

import (
	"errors"
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
