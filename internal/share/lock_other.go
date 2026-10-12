//go:build !unix

package share

import (
	"errors"
	"os"
)

// sharing is off here: each bauddy opens the port itself, as before.
const sharing = false

func dir() (string, error) { return "", errors.New("sharing isn't supported on this system") }

func lock(string) (*os.File, error) { return nil, errors.New("sharing isn't supported on this system") }
