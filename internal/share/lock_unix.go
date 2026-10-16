//go:build unix

package share

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// sharing reports whether consoles can be shared on this system.
const sharing = true

// dir returns the folder for sockets and locks, made if needed and
// checked to be this user's alone: $XDG_RUNTIME_DIR/bauddy, else
// bauddy-<uid> in the temp folder.
func dir() (string, error) {
	d := filepath.Join(os.TempDir(), fmt.Sprintf("bauddy-%d", os.Getuid()))
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		d = filepath.Join(x, "bauddy")
	}
	if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	// It may have been made by someone else first, in a shared /tmp.
	var st unix.Stat_t
	if err := unix.Lstat(d, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Getuid() || st.Mode&0o077 != 0 {
		return "", fmt.Errorf("%s isn't a folder only you can use, so this console can't be shared", d)
	}
	return d, nil
}

// lock takes the lock file at path, or returns errHeld if another process
// has it. The lock is released when the file is closed or the process
// ends, so it can't go stale. The holder removes the file when it lets go
// (unlock), so if the file locked here has gone from path meanwhile, it
// was a holder's that just let go: try again with a fresh one.
func lock(path string) (*os.File, error) {
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, err
		}
		lockOpened()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			f.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, errHeld
			}
			return nil, err
		}
		locked, err1 := f.Stat()
		now, err2 := os.Lstat(path)
		if err1 == nil && err2 == nil && os.SameFile(locked, now) {
			return f, nil
		}
		f.Close()
	}
}

// lockOpened runs between opening a lock file and locking it, the moment
// a holder can let go and remove the file; tests pause there.
var lockOpened = func() {}

// unlock lets go of a lock and removes its file, so the folder doesn't
// fill with one per console ever opened. Removed while still held, so
// whoever opens the path next gets a new file (see lock).
func unlock(f *os.File) {
	_ = os.Remove(f.Name())
	f.Close()
}
