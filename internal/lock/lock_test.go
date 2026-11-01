package lock

import (
	"context"
	"path/filepath"
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
