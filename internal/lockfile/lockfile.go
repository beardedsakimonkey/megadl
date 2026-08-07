// Package lockfile is an advisory lock between megadl instances, for work
// only one of them may do at a time.
package lockfile

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// Lock is an exclusive flock on a file. The kernel ties it to the open file
// description rather than to the process, so it goes away when the instance
// holding it exits however it exits: a killed megadl never leaves the lock
// standing. The file itself is only a name to lock on — nothing is read from
// it, and it is left behind on purpose so the name stays stable.
type Lock struct {
	mu   sync.Mutex
	path string
	f    *os.File // open only while the lock is held
}

func New(path string) *Lock { return &Lock{path: path} }

// TryAcquire takes the lock if it is free and reports whether this instance
// now holds it. It never waits: another instance holding the lock is an
// answer, not a delay. Taking a lock this instance already holds is a no-op.
func (l *Lock) TryAcquire() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		return true, nil
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil // held elsewhere
		}
		return false, err
	}
	l.f = f
	return true, nil
}

// Release lets the lock go, so another instance can take it. Releasing a lock
// this instance does not hold is a no-op.
func (l *Lock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
