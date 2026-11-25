package acp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
)

func TestACPSolAPIIdleListenerWakesSameSession(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			var calls atomic.Int32
			busy := make(chan struct{})
			bodies := make(chan string, 8)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				bodies <- string(body)
				w.Header().Set("Content-Type", "application/json")
				if n == 1 {
					input := map[string]string{"action": "register", "code": `let previous=null;while(true){const value=sol.readText("event.txt");if(value!==null&&value!==previous){previous=value;sol.trigger("A file event happened: "+value);}await sol.sleep(25);}`}
					response := map[string]any{"id": "registered", "type": "message", "role": "assistant", "model": "test", "content": []any{map[string]any{"type": "tool_use", "id": "watch", "name": "solapi", "input": input}}, "stop_reason": "tool_use", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
					_ = json.NewEncoder(w).Encode(response)
					return
				}
				if n == 2 {
					select {
					case <-busy:
					case <-r.Context().Done():
						return
					}
				}
				_, _ = io.WriteString(w, `{"id":"done","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"Handled"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer endpoint.Close()
			cwd := t.TempDir()
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg.BaseURL, cfg.APIFormat, cfg.Model, cfg.Stream = endpoint.URL, "anthropic", "test", false
				cfg.PermissionMode, cfg.Permissions.Mode = permission.ModeUltra, permission.ModeUltra
				cfg.Memory.Enabled = false
				return cfg
			}, nil)
			if version == 2 {
				initializeV2Test(t, client, false)
			} else {
				mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
			}
			var created SessionNewResult
			raw := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": cwd})
			if err := json.Unmarshal(raw, &created); err != nil {
				t.Fatal(err)
			}
			id := created.SessionID
			sess := client.server.getSession(id)
			result := make(chan struct{})
			go func() {
				defer close(result)
				mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": id, "prompt": []ContentBlock{{Type: "text", Text: "Watch for file event"}}})
			}()
			waitFor(t, func() bool { return calls.Load() == 2 })
			// The original turn is still busy. Firing a trigger must only queue it.
			if err := os.WriteFile(filepath.Join(cwd, "event.txt"), []byte("ready"), 0600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(75 * time.Millisecond)
			if calls.Load() != 2 {
				t.Fatal("trigger overlapped active model turn")
			}
			close(busy)
			waitFor(t, func() bool { sess.mu.Lock(); defer sess.mu.Unlock(); return calls.Load() == 3 && !sess.prompting })
			select {
			case <-result:
			case <-time.After(time.Second):
				t.Fatal("original prompt did not finish")
			}
			// No active prompt remains now. The listener must continue without model polling.
			time.Sleep(50 * time.Millisecond)
			if calls.Load() != 3 {
				t.Fatal("idle listener polled the model")
			}
			if err := os.WriteFile(filepath.Join(cwd, "event.txt"), []byte("after-idle"), 0600); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { sess.mu.Lock(); defer sess.mu.Unlock(); return calls.Load() == 4 && !sess.prompting })
			if client.server.getSession(id) != sess {
				t.Fatal("trigger opened a different session")
			}
			transcript, err := client.server.loadPersistedSession(t.Context(), sess, true)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(transcript.Messages)
			if !strings.Contains(string(data), "A file event happened") {
				t.Fatalf("trigger prompt missing from history: %s", data)
			}
			var last string
			for len(bodies) > 0 {
				last = <-bodies
			}
			if !strings.Contains(last, "Watch for file event") || !strings.Contains(last, "A file event happened") {
				t.Fatal("trigger did not resume same conversation")
			}
			if version == 2 {
				sess.mu.Lock()
				records := append([]v2PromptRecord(nil), sess.v2Prompts...)
				sess.mu.Unlock()
				if len(records) != 3 || records[1].Fingerprint == "" || records[2].Fingerprint == "" {
					t.Fatalf("trigger prompt not durably linked: %+v", records)
				}
			}
		})
	}
}

func TestACPSolAPICancelStopsCurrentRunAndListeners(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		input := map[string]string{"action": "register", "code": `while(true){await sol.sleep(1000);}`}
		response := map[string]any{"id": "cancel", "type": "message", "role": "assistant", "model": "test", "content": []any{
			map[string]any{"type": "tool_use", "id": "register", "name": "solapi", "input": input},
			map[string]any{"type": "tool_use", "id": "cancel", "name": "solapi", "input": map[string]string{"action": "cancel"}},
		}, "stop_reason": "tool_use", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer endpoint.Close()
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.BaseURL, cfg.APIFormat, cfg.Model, cfg.Stream = endpoint.URL, "anthropic", "test", false
		cfg.PermissionMode, cfg.Permissions.Mode = permission.ModeUltra, permission.ModeUltra
		cfg.Memory.Enabled = false
		return cfg
	}, nil)
	initializeV2Test(t, client, false)
	var created SessionNewResult
	_ = json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created)
	id := created.SessionID
	sess := client.server.getSession(id)
	mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": id, "prompt": []ContentBlock{{Type: "text", Text: "Cancel current run"}}})
	waitFor(t, func() bool { sess.mu.Lock(); defer sess.mu.Unlock(); return calls.Load() > 0 && !sess.prompting })
	if calls.Load() != 1 {
		t.Fatal("cancelled run made another model call")
	}
	if client.server.getSession(id) == nil {
		t.Fatal("cancel deleted/closed current session")
	}
	transcript, err := client.server.loadPersistedSession(t.Context(), sess, true)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(transcript.Messages)
	if !strings.Contains(string(data), "Cancel current run") {
		t.Fatal("cancel removed history")
	}
	if _, ok := sess.application.NextSessionTrigger(sess.persistID(), sess.workDir); ok {
		t.Fatal("listener survived cancellation")
	}
}
