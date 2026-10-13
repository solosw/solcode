package engine

import (
	"context"
	"testing"
)

func TestWorkingSetInjectionAllowedLocalTaskBoundaries(t *testing.T) {
	ctx := context.Background()
	if !workingSetInjectionAllowed(ctx, true, true, nil) {
		t.Fatal("a changed task with useful state should inject without Jev")
	}
	if workingSetInjectionAllowed(ctx, false, true, nil) {
		t.Fatal("ordinary tool rounds must not repeat the working set")
	}
	if workingSetInjectionAllowed(ctx, true, false, nil) {
		t.Fatal("a goal-only working set must not duplicate the user prompt")
	}
}

func TestWorkingSetInjectionAllowedAsksJevOnlyForUsefulChangedTask(t *testing.T) {
	ctx := context.Background()
	calls := 0
	jev := func(context.Context) bool {
		calls++
		return true
	}
	if !workingSetInjectionAllowed(ctx, true, true, jev) {
		t.Fatal("Jev allow decision should inject")
	}
	if workingSetInjectionAllowed(ctx, false, true, jev) || workingSetInjectionAllowed(ctx, true, false, jev) {
		t.Fatal("unchanged/empty working sets must not inject")
	}
	if calls != 1 {
		t.Fatalf("Jev calls = %d, want 1", calls)
	}
	if workingSetInjectionAllowed(ctx, true, true, func(context.Context) bool { return false }) {
		t.Fatal("Jev deny decision must skip task-switch injection")
	}
}
