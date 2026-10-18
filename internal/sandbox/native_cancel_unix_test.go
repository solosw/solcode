//go:build !windows

package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunDeadlineAfterShellExit(t *testing.T) {
	sb, err := NewWithPolicy(t.TempDir(), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sb.Run(ctx, Command{Program: "sh", Args: []string{"-c", "sleep 5 & echo ready"}})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sandbox stayed blocked on inherited output pipes after deadline")
	}
}
