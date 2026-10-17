package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestFileStoreConcurrentReadersSeeCompleteSnapshots(t *testing.T) {
	store := NewFileStore(t.TempDir())
	saved := NewSession("atomic", "", "")
	saved.Messages = []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("initial"))}
	if err := store.Save(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 1)
	wg.Go(func() {
		for ctx.Err() == nil {
			if _, err := store.Load(context.Background(), "atomic"); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	})
	for i := 0; i < 15; i++ {
		saved.Messages = []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("snapshot ", 15_000)))}
		if err := store.Save(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("reader saw partial JSON: %v", err)
	default:
	}
	saved.Subagent = json.RawMessage("invalid JSON")
	if err := store.Save(t.Context(), saved); err == nil {
		t.Fatal("expected marshal error")
	}
	if _, err := store.Load(t.Context(), "atomic"); err != nil {
		t.Fatalf("failed save damaged previous snapshot: %v", err)
	}
}
