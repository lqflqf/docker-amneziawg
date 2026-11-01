// Package lock serializes awgctl and the tunnel service on one flock file.
// svc-amneziawg takes the same lock with the busybox flock command.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock is a held lock.
type Lock struct{ f *os.File }

// Acquire takes an exclusive lock on path. It waits until ctx ends.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f}, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("another awgctl command or the tunnel service holds %s: %w", path, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Release frees the lock.
func (l *Lock) Release() {
	if l != nil && l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}
