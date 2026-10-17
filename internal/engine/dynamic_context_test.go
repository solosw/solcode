package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func contextOccurrences(messages []sdk.MessageParam, needle string) int {
	count := 0
	for _, message := range messages {
		for _, block := range message.Content {
			if block.OfText != nil {
				count += strings.Count(block.OfText.Text, needle)
			}
		}
	}
	return count
}

func requireMessagePrefix(t *testing.T, before, after []sdk.MessageParam) {
	t.Helper()
	if len(after) < len(before) || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatal("dynamic context moved or rewrote the retained conversation prefix")
	}
}

func TestDynamicContextMaterializesOnlyChangedSections(t *testing.T) {
	original := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("user task"))}
	d := newDynamicContext(original)
	builder := ContextBuilder{ModeInstructions: "mode", AgentInstructions: "Explorer role", ForceSkill: "skill", FoldedTools: "tools", WorkingSet: "files", EfficiencyNote: "hint"}
	req := RunRequest{SessionSummary: "summary", ProjectKnowledge: "knowledge", MemoryContext: []ContextItem{{Content: "memory"}}}
	first := d.append(original, builder, req, false)
	requireMessagePrefix(t, original, first)
	if got := d.append(first, builder, req, false); !reflect.DeepEqual(got, first) {
		t.Fatal("unchanged sections must not generate another context message")
	}
	builder.ModeInstructions = "updated mode"
	second := d.append(first, builder, req, false)
	requireMessagePrefix(t, first, second)
	if len(second) != len(first)+1 || len(second[len(second)-1].Content) != 1 {
		t.Fatal("mode changes should append only the changed section")
	}
	if got := newDynamicContext(second).append(second, builder, req, false); !reflect.DeepEqual(got, second) {
		t.Fatal("session resume must recognize materialized overlays")
	}
	cleared := newDynamicContext(second).append(second, ContextBuilder{ModeInstructions: "updated mode"}, RunRequest{}, true)
	for _, kind := range []string{"agent-role", "skill", "folded-tools", "working-set"} {
		if !messagesContain(cleared[len(second):], dynamicContextClears[kind]) {
			t.Fatalf("missing reset for %s", kind)
		}
	}
	if got := newDynamicContext(cleared).append(cleared, ContextBuilder{ModeInstructions: "updated mode"}, RunRequest{}, true); !reflect.DeepEqual(got, cleared) {
		t.Fatal("resumed empty-state notices should not repeat")
	}
}

func TestEfficiencyInjectionCoalescesChangingCounters(t *testing.T) {
	var gate efficiencyInjection
	if got := gate.next(2, "file (read 2×)"); got == "" {
		t.Fatal("first signal must be immediate")
	}
	if got := gate.next(3, "file (read 3×)"); got != "" {
		t.Fatal("counter changes must respect cooldown")
	}
	if got := gate.next(6, "file (read 3×)"); got == "" {
		t.Fatal("pending changed signal should emit after cooldown")
	}
	if got := gate.next(10, "file (read 3×)"); got != "" {
		t.Fatal("unchanged signals must not repeat")
	}
	if got := gate.next(11, ""); got != "" {
		t.Fatal("progress must not generate an empty advisory")
	}
}

func dynamicTestRegistry(t *testing.T) (*tool.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n// second line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewBatchTool(), tool.NewViewTool())
	return registry, dir
}

func dynamicReadCall(id string, offset int) map[string]any {
	return backgroundCall(id, tool.BatchToolName, map[string]any{
		"operations": []any{map[string]any{"tool": tool.ViewToolName, "input": map[string]any{"path": "a.go", "offset": offset, "limit": 1}}},
	})
}

func TestEngineInjectsAntiLoopContextAfterResultsNotAtRunStart(t *testing.T) {
	registry, dir := dynamicTestRegistry(t)
	var requests []cpanthropic.MessageRequest
	jevCalls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if len(requests) > 0 {
			requireMessagePrefix(t, requests[len(requests)-1].Messages, req.Messages)
			if requests[0].System != req.System {
				t.Fatal("dynamic injection changed the system prefix")
			}
		}
		requests = append(requests, req)
		switch len(requests) {
		case 1, 2:
			if messagesContain(req.Messages, "Task working set") || messagesContain(req.Messages, "Efficiency note") {
				t.Fatal("goal/ordinary read must not inject a redundant working set")
			}
			return backgroundMessage(t, dynamicReadCall(fmt.Sprintf("read%d", len(requests)), 0)), nil
		default:
			if !messagesContain(req.Messages, "Efficiency note") || !messagesContain(req.Messages, "Task working set") {
				t.Fatal("repeated Batch reads must trigger useful local context")
			}
			if len(backgroundResultTexts(req.Messages, "read2")) != 1 {
				t.Fatal("injection lost the tool result")
			}
			// The new context follows the tool-result message, never splits a
			// tool_use/tool_result pair or inserts before the result.
			last := len(req.Messages) - 1
			if req.Messages[last-1].Content[0].OfToolResult == nil || req.Messages[last].Content[0].OfText == nil {
				t.Fatal("overlay should be appended after completed tool results")
			}
			return backgroundMessage(t), nil
		}
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 4, ShouldInjectWorkingSet: func(context.Context, map[string]any) bool { jevCalls++; return false }})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "read a.go", WorkDir: dir}})
	if result.AgentResult.Error != "" || len(requests) != 3 || jevCalls != 0 {
		t.Fatalf("error=%q requests=%d Jev=%d", result.AgentResult.Error, len(requests), jevCalls)
	}
	if contextOccurrences(result.Messages, "Task working set") != 1 || contextOccurrences(result.Messages, "Efficiency note") != 1 {
		t.Fatal("injected context must remain in durable history exactly once")
	}
}

func TestEngineQueuedTaskUsesLatestGoalAndOptionalDecision(t *testing.T) {
	for _, decision := range []string{"local", "allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			registry, dir := dynamicTestRegistry(t)
			queued, calls, jevCalls := false, 0, 0
			config := Config{Tools: registry, MaxTurns: 4}
			config.QueuedPrompts = func() []string {
				if queued {
					queued = false
					return []string{"new objective"}
				}
				return nil
			}
			if decision != "local" {
				config.ShouldInjectWorkingSet = func(_ context.Context, state map[string]any) bool {
					jevCalls++
					if state["task"] != "new objective" || state["reason"] != "task_changed" {
						t.Fatalf("decision used stale task: %+v", state)
					}
					return decision == "allow"
				}
			}
			config.Client = backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				if calls == 1 {
					queued = true
					return backgroundMessage(t, dynamicReadCall("read", 0)), nil
				}
				want := decision != "deny"
				if messagesContain(req.Messages, "Goal: new objective") != want {
					t.Fatalf("queued task working set mismatch for %s", decision)
				}
				if calls == 2 {
					return backgroundMessage(t, dynamicReadCall("next", 1)), nil
				}
				return backgroundMessage(t), nil
			})
			result := NewEngine(config).Run(t.Context(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "old task", WorkDir: dir})
			wantJev := 1
			if decision == "local" {
				wantJev = 0
			}
			if result.Error != "" || calls != 3 || jevCalls != wantJev {
				t.Fatalf("error=%q calls=%d decisions=%d", result.Error, calls, jevCalls)
			}
		})
	}
}

func TestEngineModeChangesAppendAndDeactivateOldPlanOverlay(t *testing.T) {
	registry, dir := dynamicTestRegistry(t)
	permissions := permission.NewService(permission.ModePlan)
	calls := 0
	var previous []sdk.MessageParam
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			if !messagesContain(req.Messages, "(ACTIVE)") {
				t.Fatal("missing plan overlay")
			}
			previous = req.Messages
			permissions.SetMode(permission.ModeAuto)
			return backgroundMessage(t, dynamicReadCall("read", 0)), nil
		}
		requireMessagePrefix(t, previous, req.Messages)
		if !messagesContain(req.Messages[len(previous):], "Plan mode is inactive") {
			t.Fatal("leaving plan mode must explicitly supersede its durable overlay")
		}
		return backgroundMessage(t), nil
	})
	result := NewEngine(Config{Client: client, Tools: registry, Permissions: permissions, MaxTurns: 3}).Run(t.Context(), agent.AgentConfig{ID: "main", Prompt: "read", WorkDir: dir})
	if result.Error != "" || calls != 2 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestEngineCompactionRestoresWorkingSetAndKeepsStreamingCallbacks(t *testing.T) {
	registry, dir := dynamicTestRegistry(t)
	calls, compactions, jevCalls := 0, 0, 0
	config := Config{
		Tools: registry, MaxTurns: 4, MaxContextTokens: 1,
		OnTextDelta: func(string) {}, OnThinkingDelta: func(string) {},
		ShouldInjectWorkingSet: func(context.Context, map[string]any) bool { jevCalls++; return false },
		CompactMessages: func(_ context.Context, _ []sdk.MessageParam) ([]sdk.MessageParam, error) {
			compactions++
			return []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("fresh compacted summary"))}, nil
		},
	}
	config.Client = backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if req.OnTextDelta == nil || req.OnThinkingDelta == nil {
			t.Fatal("compaction must not drop main-agent streaming callbacks")
		}
		if messagesContain(req.Messages, "old session summary") || messagesContain(req.Messages, "old memory") || messagesContain(req.Messages, "old project knowledge") {
			t.Fatal("pre-compaction request fields were injected over the durable summary")
		}
		if calls > 1 {
			if contextOccurrences(req.Messages, "Task working set") != 1 || !messagesContain(req.Messages, filepath.Join(dir, "a.go")) {
				t.Fatal("compaction recovery must restore file metadata exactly once")
			}
			if messagesContain(req.Messages, "Efficiency note") || messagesContain(req.Messages, "package a") {
				t.Fatal("recovery must not forbid necessary re-reads or dump cached code")
			}
		}
		if calls < 3 {
			return backgroundMessage(t, dynamicReadCall(fmt.Sprintf("read%d", calls), 0)), nil
		}
		return backgroundMessage(t), nil
	})
	result := NewEngine(config).RunWithHistory(t.Context(), RunRequest{
		AgentConfig:    agent.AgentConfig{ID: "main", Prompt: "task", WorkDir: dir},
		SessionSummary: "old session summary", ProjectKnowledge: "old project knowledge",
		MemoryContext: []ContextItem{{Content: "old memory"}},
	})
	if result.AgentResult.Error != "" || calls != 3 || compactions != 3 || jevCalls != 0 {
		t.Fatalf("error=%q calls=%d compactions=%d Jev=%d", result.AgentResult.Error, calls, compactions, jevCalls)
	}
}

func TestEfficiencyInjectionNewErrorsDoNotWaitForCounterCooldown(t *testing.T) {
	var gate efficiencyInjection
	gate.next(2, "file (read 2×)")
	if got := gate.next(3, "file (read 3×)\nError already seen: permission denied (2×)"); got == "" {
		t.Fatal("new errors should be surfaced at the next boundary, not delayed by a read-warning cooldown")
	}
}

func TestEngineBackgroundCompletionDoesNotTriggerTaskChangeInjection(t *testing.T) {
	registry, dir := dynamicTestRegistry(t)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	registry.Register(&backgroundTestTool{name: tool.BashToolName, run: func(ctx context.Context) *tool.ContentBlock {
		select {
		case <-release:
			return tool.Result("background done")
		case <-ctx.Done():
			return tool.ErrorResult(ctx.Err().Error())
		}
	}})
	calls, jevCalls := 0, 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("long", tool.BashToolName, map[string]any{})), nil
		case 2:
			return backgroundMessage(t, dynamicReadCall("read", 0)), nil
		case 3:
			unblock()
			return backgroundMessage(t), nil
		default:
			if !messagesContain(req.Messages, "<background-task-notification>") {
				t.Fatal("missing background completion")
			}
			if messagesContain(req.Messages, "Task working set") {
				t.Fatal("completion alone is not a new user task")
			}
			return backgroundMessage(t), nil
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result := NewEngine(Config{
		Client: client, Tools: registry, MaxTurns: 5,
		BackgroundTasksEnabled: true, BackgroundYieldAfter: 5 * time.Millisecond,
		ShouldInjectWorkingSet: func(context.Context, map[string]any) bool { jevCalls++; return true },
	}).Run(ctx, agent.AgentConfig{ID: "main", Prompt: "independent work", WorkDir: dir})
	if result.Error != "" || calls != 4 || jevCalls != 0 {
		t.Fatalf("result=%+v calls=%d Jev=%d", result, calls, jevCalls)
	}
}

func TestDynamicContextSameAdvisoryInNewTaskIsANewEvent(t *testing.T) {
	d := newDynamicContext(nil)
	messages := d.append(nil, ContextBuilder{EfficiencyNote: "repeat read"}, RunRequest{}, false)
	messages = d.append(messages, ContextBuilder{}, RunRequest{}, true)
	messages = d.append(messages, ContextBuilder{EfficiencyNote: "repeat read"}, RunRequest{}, false)
	if contextOccurrences(messages, "repeat read") != 2 {
		t.Fatal("a new task's repeat signal must not be deduplicated against the old task's counters")
	}
}
