package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSecondHolderWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgctl.lock")
	l1, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); err == nil {
		t.Fatal("a second holder got the lock")
	}
	l1.Release()
	l2, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}

func TestAcquireErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := Acquire(context.Background(), filepath.Join(file, "awgctl.lock")); err == nil {
		t.Error("a lock under a regular file was accepted")
	}
	link := filepath.Join(dir, "link.lock")
	os.Symlink(file, link)
	if _, err := Acquire(context.Background(), link); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("a symlinked lock file: got %v, want ELOOP", err)
	}
}

func TestReleaseTwice(t *testing.T) {
	l, err := Acquire(context.Background(), filepath.Join(t.TempDir(), "awgctl.lock"))
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release()
	var none *Lock
	none.Release()
}
