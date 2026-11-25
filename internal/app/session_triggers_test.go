package app

import (
	"context"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
	"github.com/solosw/solcode/internal/trigger"
)

func TestSessionTriggersScopeCancelAndHistory(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	a := &App{Config: cfg}
	defer a.closeSessionTriggers()
	wake := make(chan struct{}, 8)
	detach, err := a.AttachSessionTriggers("first", cfg.WorkDir, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	secondDetach, err := a.AttachSessionTriggers("second", cfg.WorkDir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer secondDetach()
	ctx, unbind, err := a.bindSessionTriggers(t.Context(), "first", cfg.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer unbind()
	if _, err := a.handleSolAPI(ctx, "second", cfg.WorkDir, tool.SolAPIRequest{Action: "cancel"}); err == nil {
		t.Fatal("cross-session control accepted")
	}
	if _, err := a.handleSolAPI(ctx, "first", cfg.WorkDir, tool.SolAPIRequest{Action: "register", Code: `sol.trigger("wake current");while(true){await sol.sleep(1000);}`}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(3 * time.Second):
		t.Fatal("trigger did not notify host")
	}
	history, err := a.handleSolAPI(ctx, "first", cfg.WorkDir, tool.SolAPIRequest{Action: "history", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	records := history.(map[string]any)["records"].([]trigger.Event)
	if len(records) != 2 {
		t.Fatalf("history did not include listener and event: %+v", records)
	}
	if _, ok := a.NextSessionTrigger("second", cfg.WorkDir); ok {
		t.Fatal("another session received trigger")
	}
	if _, err := a.handleSolAPI(ctx, "first", cfg.WorkDir, tool.SolAPIRequest{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("current run was not cancelled")
	}
	if _, ok := a.NextSessionTrigger("first", cfg.WorkDir); ok {
		t.Fatal("cancelled event remained queued")
	}
	other, otherUnbind, err := a.bindSessionTriggers(t.Context(), "second", cfg.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer otherUnbind()
	if other.Err() != nil {
		t.Fatal("cancel affected another session")
	}
	history, err = a.handleSolAPI(other, "second", cfg.WorkDir, tool.SolAPIRequest{Action: "history", Limit: 20})
	if err != nil || len(history.(map[string]any)["records"].([]trigger.Event)) != 0 {
		t.Fatal("history leaked to other session")
	}
}
