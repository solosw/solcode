package engine

import (
	"context"
	"testing"
)

func TestWorkingSetInjectionAllowedWithoutJevOnlyAtRunStart(t *testing.T) {
	ctx := context.Background()
	if !workingSetInjectionAllowed(ctx, 0, true, false, nil) {
		t.Fatal("without Jev, the initial turn should inject")
	}
	if workingSetInjectionAllowed(ctx, 1, true, false, nil) {
		t.Fatal("without Jev, later task changes in the same run should not inject")
	}
	if workingSetInjectionAllowed(ctx, 0, true, true, nil) {
		t.Fatal("consumed decision must not inject again")
	}
}

func TestWorkingSetInjectionAllowedAsksJevOnlyForChangedTask(t *testing.T) {
	ctx := context.Background()
	calls := 0
	jev := func(context.Context) bool {
		calls++
		return true
	}
	if !workingSetInjectionAllowed(ctx, 0, true, false, jev) {
		t.Fatal("Jev allow decision should inject")
	}
	if calls != 1 {
		t.Fatalf("Jev calls = %d, want 1", calls)
	}
	if workingSetInjectionAllowed(ctx, 1, false, false, jev) {
		t.Fatal("unchanged task must not inject")
	}
	if calls != 1 {
		t.Fatalf("Jev should not be called for unchanged task, calls = %d", calls)
	}
	if workingSetInjectionAllowed(ctx, 1, true, false, func(context.Context) bool { return false }) {
		t.Fatal("Jev deny decision must skip injection")
	}
}
