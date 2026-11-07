package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

type compressionClientFunc func(context.Context, cpanthropic.MessageRequest) (*sdk.Message, error)

func (f compressionClientFunc) Create(ctx context.Context, r cpanthropic.MessageRequest) (*sdk.Message, error) {
	return f(ctx, r)
}

func compressionArguments(t *testing.T, value any, summary string) map[string]any {
	t.Helper()
	var snapshot contextcompression.Overview
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case string:
			if i := strings.Index(x, "\n{"); i >= 0 && strings.Contains(x, "Compression is mandatory") {
				if err := json.NewDecoder(strings.NewReader(x[i+1:])).Decode(&snapshot); err != nil {
					t.Error(err)
				}
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		case map[string]any:
			for _, v := range x {
				visit(v)
			}
		}
	}
	raw, _ := json.Marshal(value)
	var wire any
	_ = json.Unmarshal(raw, &wire)
	visit(wire)
	if len(snapshot.CompressibleRanges) == 0 {
		t.Fatal("missing mandatory compression ranges")
	}
	largest := snapshot.CompressibleRanges[0]
	for _, r := range snapshot.CompressibleRanges {
		if r.Tokens > largest.Tokens {
			largest = r
		}
	}
	return map[string]any{"start": largest.Start, "end": largest.End, "summary": summary, "revision": snapshot.Revision, "topic": "Completed work"}
}

func compressionTestSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.NewSession("main", t.TempDir(), "chat")
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock("ORIGINAL_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("EXACT_TOOL_EVIDENCE ", 3000))), sdk.NewUserMessage(sdk.NewTextBlock("CURRENT_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock("task remains in progress")))
	return s
}

func newCompressionTestApp(t *testing.T, summary string, failure error) (*App, *int) {
	t.Helper()
	cfg := config.Default()
	cfg.MaxContextTokens = 200000
	calls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.ContextTools(nil, nil)...)
	client := compressionClientFunc(func(_ context.Context, r cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if r.ForceTool != "compress" || r.Stream || r.Thinking {
			t.Fatal("not a forced synchronous compress request")
		}
		if failure != nil {
			return nil, failure
		}
		args := compressionArguments(t, r.Messages, summary)
		raw, _ := json.Marshal(map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []any{map[string]any{"type": "tool_use", "id": "fold", "name": "compress", "input": args}}})
		var response sdk.Message
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		return &response, nil
	})
	return &App{Config: cfg, Tools: reg, Engine: engine.NewEngine(engine.Config{Client: client, Tools: reg, ModelName: "chat", MaxContextTokens: cfg.MaxContextTokens, MaxTokens: 4096, Permissions: permission.NewService(permission.ModeBypass)})}, &calls
}

func TestExplicitCompactUsesToolAndPreservesArchive(t *testing.T) {
	app, calls := newCompressionTestApp(t, testSessionHandoff(), nil)
	writer := &recordingSummaryWriter{err: errors.New("must not use summary backend")}
	app.summaryWriter = writer
	app.Sessions = session.NewManager(session.NewFileStore(t.TempDir()), "main")
	current := compressionTestSession(t)
	current.Summary = "background summary remains separate"
	if err := app.Sessions.Save(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	compacted, changed, err := app.CompactSession(t.Context(), "main", current.Metadata.WorkDir)
	if err != nil || !changed || *calls != 1 || writer.called {
		t.Fatalf("changed=%v calls=%d writer=%v error=%v", changed, *calls, writer.called, err)
	}
	if compacted.Summary != current.Summary || !strings.Contains(session.Transcript(compacted.Messages), "CURRENT_REQUEST") {
		t.Fatal("lost current task or rewrote background summary")
	}
	loaded, err := app.Sessions.LoadOrCreate(t.Context(), "main", current.Metadata.WorkDir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Compression == nil || loaded.Compression.Revision != 1 || !strings.Contains(session.Transcript(loaded.Messages), testSessionHandoff()) {
		t.Fatal("fold did not persist")
	}
	found, err := loaded.Compression.Search("EXACT_TOOL_EVIDENCE", 0, 10)
	if err != nil || found["total"].(int) == 0 {
		t.Fatal("archive lost", err)
	}
}

func TestExplicitCompressFailurePreservesSession(t *testing.T) {
	app, calls := newCompressionTestApp(t, "", errors.New("endpoint unavailable"))
	current := compressionTestSession(t)
	before := session.Transcript(current.Messages)
	changed, err := app.compactSession(t.Context(), current, true)
	if err == nil || !strings.Contains(err.Error(), "endpoint unavailable") || changed || *calls != 1 || session.Transcript(current.Messages) != before {
		t.Fatalf("changed=%v calls=%d err=%v", changed, *calls, err)
	}
}

func TestExplicitCompressSkipsProtectedOnlyHistory(t *testing.T) {
	app, calls := newCompressionTestApp(t, "unused", nil)
	current := session.NewSession("main", t.TempDir(), "chat")
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("CURRENT_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock("working")))
	changed, err := app.compactSession(t.Context(), current, true)
	if err != nil || changed || *calls != 0 {
		t.Fatalf("changed=%v calls=%d err=%v", changed, *calls, err)
	}
}

func TestMandatoryCompressionProtocolsAndModelSwitch(t *testing.T) {
	for _, format := range []string{"anthropic", "openai", "responses"} {
		t.Run(format, func(t *testing.T) {
			var oldCalls, forced, continued atomic.Int32
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldCalls.Add(1); http.Error(w, "old endpoint", 500) }))
			defer old.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				validKey := r.Header.Get("Authorization") == "Bearer current-key"
				if format == "anthropic" {
					validKey = r.Header.Get("x-api-key") == "current-key"
				}
				if body["model"] != "current" || !validKey {
					t.Error("wrong model/credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				if body["tool_choice"] != nil {
					forced.Add(1)
					if body["stream"] == true || body["thinking"] != nil {
						t.Error("compression must not stream or think")
					}
					args := compressionArguments(t, body, "Previous work completed. Continue CURRENT_REQUEST; exact evidence is archived.")
					raw, _ := json.Marshal(args)
					switch format {
					case "anthropic":
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_fold", "type": "message", "role": "assistant", "model": "current", "stop_reason": "tool_use", "content": []any{map[string]any{"type": "tool_use", "id": "fold", "name": "compress", "input": args}}})
					case "openai":
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "fold", "type": "function", "function": map[string]any{"name": "compress", "arguments": string(raw)}}}}, "finish_reason": "tool_calls"}}})
					case "responses":
						_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "fold", "name": "compress", "arguments": string(raw)}}})
					}
				} else {
					continued.Add(1)
					raw, _ := json.Marshal(body)
					if !strings.Contains(string(raw), "CURRENT_REQUEST") || strings.Contains(string(raw), "EXACT_TOOL_EVIDENCE") {
						t.Error("continuation lost task or retained original evidence")
					}
					switch format {
					case "anthropic":
						_, _ = w.Write([]byte("{\"id\":\"msg_done\",\"type\":\"message\",\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"task done\"}]}"))
					case "openai":
						_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"task done\"},\"finish_reason\":\"stop\"}]}"))
					case "responses":
						_, _ = w.Write([]byte("{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"task done\"}]}]}"))
					}
				}
			}))
			defer endpoint.Close()
			cfg := config.Default()
			cfg.WorkDir = t.TempDir()
			cfg.Model, cfg.APIFormat, cfg.APIKey, cfg.BaseURL = "old", format, "old-key", old.URL
			cfg.MaxContextTokens, cfg.MaxTokens, cfg.SystemPrompt = 200000, 4096, "Help with coding."
			cfg.Memory.CompactionTriggerPercent = 10
			cfg.Stream = false
			cfg.Memory.Enabled, cfg.Session.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
			cfg.Memory.Organizer.Enabled = false
			disabled := false
			cfg.CodeGraph.Enabled = &disabled
			app, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			cfg.Model, cfg.APIKey, cfg.BaseURL = "current", "current-key", endpoint.URL
			app.UpdateModelForNextCall(cfg)
			history := compressionTestSession(t)
			result := app.Engine.RunWithHistory(t.Context(), engine.RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: cfg.WorkDir, Prompt: "CURRENT_REQUEST", MaxTurns: 1}, Messages: history.Messages})
			if result.AgentResult.Error != "" || result.AgentResult.Output != "task done" || oldCalls.Load() != 0 || forced.Load() != 1 || continued.Load() != 1 {
				t.Fatalf("result=%+v old=%d forced=%d continuation=%d", result.AgentResult, oldCalls.Load(), forced.Load(), continued.Load())
			}
		})
	}
}
