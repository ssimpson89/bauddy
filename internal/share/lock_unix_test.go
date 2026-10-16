//go:build unix

package share

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A console's lock file goes when its holder lets go, so the folder
// doesn't fill with one per console ever opened, and the next bauddy
// locks a new one.
func TestLockFilesDontPileUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.lock")
	a, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(path); !errors.Is(err, errHeld) {
		t.Fatalf("second lock while held: %v, want errHeld", err)
	}
	unlock(a)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after unlock the lock file is still there (%v)", err)
	}
	b, err := lock(path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	defer unlock(b)
	if _, err := lock(path); !errors.Is(err, errHeld) {
		t.Errorf("the new lock isn't exclusive: %v", err)
	}
}

// The moment unlock removes the file can fall between a contender opening
// it and locking it. The contender then locks a file nobody else uses, so
// it must notice and try again, and find the console's new holder.
func TestALockFileRemovedMidLockIsTriedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.lock")
	a, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, resume := make(chan struct{}), make(chan struct{})
	first := true
	lockOpened = func() {
		if first { // only the contender's first try pauses
			first = false
			close(opened)
			<-resume
		}
	}
	t.Cleanup(func() { lockOpened = func() {} })
	got := make(chan error, 1)
	go func() {
		f, err := lock(path)
		if err == nil {
			f.Close()
		}
		got <- err
	}()
	<-opened // the contender has the old file open, not yet locked
	unlock(a)
	lockOpened = func() {}
	b, err := lock(path) // a new holder, on a new file
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(b)
	close(resume)
	if err := <-got; !errors.Is(err, errHeld) {
		t.Errorf("contender: %v; want errHeld, not a second holder", err)
	}
}
