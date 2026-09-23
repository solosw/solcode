package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/workflow"
)

// TestBuildJevDisabledReturnsNil pins the default: with Jev off there is no
// runtime, and every consumer must treat nil as "feature absent".
func TestBuildJevDisabledReturnsNil(t *testing.T) {
	jev, err := buildJev(config.Default())
	if err != nil {
		t.Fatalf("buildJev() = %v", err)
	}
	if jev != nil {
		t.Fatalf("jev = %+v, want nil when disabled", jev)
	}
	// A nil runtime must still answer every accessor safely.
	var nilRuntime *jevRuntime
	if nilRuntime.router() != nil || nilRuntime.guardrail() != nil {
		t.Fatal("a nil runtime must not produce router or guardrail")
	}
	if nilRuntime.memoryJudge() != nil || nilRuntime.memoryExtractor() != nil {
		t.Fatal("a nil runtime must not produce memory judge or extractor")
	}
	if got := nilRuntime.SuggestWorkflow(context.Background(), "do a thing", nil); got != "" {
		t.Fatalf("SuggestWorkflow = %q", got)
	}
}

// Enabled but keyless must behave exactly like disabled, since every call would
// otherwise fail and the fallbacks would fire on every decision.
func TestBuildJevWithoutAPIKeyReturnsNil(t *testing.T) {
	cfg := config.Default()
	cfg.Jev.Enabled = true
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatalf("buildJev() = %v", err)
	}
	if jev != nil {
		t.Fatalf("jev = %+v, want nil without an api key", jev)
	}
}

// type=local with a real OpenJev model_dir enables the runtime. The inference
// engine is still a stub, so Ask fails and Decider falls back — but routing
// toggles and the evaluator seam are live.
func TestBuildJevLocalUsesOpenJevArtifacts(t *testing.T) {
	modelDir := os.ExpandEnv(`C:\Users\solosw\.solcode\models\open-jev-deberta-v3-large`)
	if _, err := os.Stat(modelDir); err != nil {
		t.Skip("open-jev artifacts not downloaded:", err)
	}
	cfg := config.Default()
	cfg.Jev = config.JevConfig{
		Enabled:  true,
		Type:     config.JevBackendLocal,
		Model:    "open-jev-deberta-v3-large",
		ModelDir: modelDir,
		DType:    "q4",
		Routing:  true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.JevType() != config.JevBackendLocal {
		t.Fatalf("JevType = %q", cfg.JevType())
	}
	if cfg.Jev.Engine != "ort" {
		t.Fatalf("Engine = %q, want ort default", cfg.Jev.Engine)
	}
	if !cfg.JevEnabled() {
		t.Fatal("local with artifacts should enable Jev")
	}
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatalf("buildJev() = %v", err)
	}
	if jev == nil {
		t.Fatal("expected a local jev runtime")
	}
	if jev.router() == nil {
		t.Fatal("routing toggle should enable the router")
	}
	// Default engine is ORT: Choose should return a real answer, not the
	// deterministic fallback (confidence 0).
	got, conf := jev.decider.Choose(context.Background(), "state", "which?",
		map[string]string{"a": "A", "b": "B"}, 0.6, "a")
	if got == "" {
		t.Fatal("Choose returned empty with local ORT")
	}
	if conf == 0 {
		t.Fatalf("Choose = %q/%v, want a non-zero confidence from ORT", got, conf)
	}
}

func TestBuildJevLocalMissingArtifactsReturnsNil(t *testing.T) {
	cfg := config.Default()
	cfg.Jev = config.JevConfig{
		Enabled:  true,
		Type:     config.JevBackendLocal,
		Model:    "missing-model",
		ModelDir: t.TempDir(),
		DType:    "q4",
		Routing:  true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.JevEnabled() {
		t.Fatal("empty model_dir without artifacts must stay disabled")
	}
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatalf("buildJev() = %v", err)
	}
	if jev != nil {
		t.Fatalf("jev = %+v, want nil without artifacts", jev)
	}
}

func TestBuildJevHonorsSubsystemToggles(t *testing.T) {
	cfg := config.Default()
	cfg.Jev = config.JevConfig{
		Enabled:     true,
		APIKey:      "k",
		Routing:     true,
		MemoryJudge: true,
		Guardrail:   true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if jev == nil {
		t.Fatal("expected a runtime")
	}
	if jev.router() == nil {
		t.Fatal("routing toggle did not enable the router")
	}
	if jev.memoryJudge() == nil || jev.memoryExtractor() == nil {
		t.Fatal("memory_judge toggle did not enable judge and extractor")
	}
	if jev.guardrail() == nil {
		t.Fatal("guardrail toggle did not enable the guardrail")
	}

	// Only routing on: the other subsystems stay off.
	onlyRouting := config.Default()
	onlyRouting.Jev = config.JevConfig{Enabled: true, APIKey: "k", Routing: true}
	if err := onlyRouting.Normalize(); err != nil {
		t.Fatal(err)
	}
	jev, err = buildJev(onlyRouting)
	if err != nil {
		t.Fatal(err)
	}
	if jev.router() == nil {
		t.Fatal("expected the router")
	}
	if jev.guardrail() != nil {
		t.Fatal("guardrail should stay off unless enabled")
	}
	if jev.memoryJudge() != nil {
		t.Fatal("memory judge should stay off unless enabled")
	}
}

// The guardrail must only inspect tools that can change state, and must never
// block a read-only tool.
func TestJevGuardrailOnlyGuardsMutatingTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("read-only tools must not trigger a guardrail request")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers": {}}`))
	}))
	defer server.Close()

	guard := &jevGuardrail{guardrail: systemone.Guardrail{
		Decider: systemone.NewDecider(systemone.NewClient(systemone.Options{
			BaseURL: server.URL, APIKey: "k", HTTPClient: server.Client(),
		})),
	}}
	for _, name := range []string{"View", "Grep", "Glob", "LS", "ReadMemory"} {
		if message := guard.CheckToolCall(context.Background(), name, json.RawMessage(`{}`)); message != "" {
			t.Fatalf("tool %s should not be guarded, got %q", name, message)
		}
	}
}

// An unreachable guardrail must hold the call, never silently allow it.
func TestJevGuardrailFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer server.Close()

	guard := &jevGuardrail{guardrail: systemone.Guardrail{
		Decider: systemone.NewDecider(systemone.NewClient(systemone.Options{
			BaseURL: server.URL, APIKey: "k", HTTPClient: server.Client(),
		})),
	}}
	message := guard.CheckToolCall(context.Background(), "Bash", json.RawMessage(`{"command":"go test ./..."}`))
	if message == "" {
		t.Fatal("an unavailable guardrail must withhold the call, not allow it")
	}
}

// The guardrail payload extraction must read the risky field, not the JSON
// envelope, for each guarded tool shape.
func TestGuardrailPayloadExtractsCommandAndContent(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"bash command", "Bash", `{"command":"rm -rf /tmp/x"}`, "rm -rf /tmp/x"},
		{"write content", "Write", `{"path":"a.go","content":"package a"}`, "a.go\npackage a"},
		{"edit strings", "Edit", `{"path":"a.go","old_string":"x","new_string":"y"}`, "a.go\nx\ny"},
		{"patch text", "Patch", `{"path":"a.go","patch_text":"@@ -1 +1 @@"}`, "a.go\n@@ -1 +1 @@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := guardrailPayload(tc.tool, json.RawMessage(tc.input)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGuardrailPayloadReadsMultiEditAndMultiWrite(t *testing.T) {
	multiEdit := `{"edits":[{"path":"a.go","old_string":"x","new_string":"y"}]}`
	got := guardrailPayload("MultiEdit", json.RawMessage(multiEdit))
	if got == "" || !containsAll(got, "a.go", "x", "y") {
		t.Fatalf("MultiEdit payload = %q", got)
	}

	multiWrite := `{"files":[{"path":"b.go","content":"package b"}]}`
	got = guardrailPayload("MultiWrite", json.RawMessage(multiWrite))
	if got == "" || !containsAll(got, "b.go", "package b") {
		t.Fatalf("MultiWrite payload = %q", got)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// SuggestWorkflow must rank only the workflows actually loaded, and must be
// inert when Jev is off.
func TestSuggestWorkflowRanksLoadedWorkflows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id := range body.Questions {
			answers[id] = map[string]any{
				"type":       systemone.TypeChoice,
				"choice":     "test-then-review",
				"confidence": 0.9,
				"probabilities": map[string]float64{
					"test-then-review": 0.9,
					"none":             0.1,
				},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Jev = config.JevConfig{Enabled: true, APIKey: "k", Routing: true}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Point the built decider at the stub by rebuilding it against the server.
	jev.decider = systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL: server.URL, APIKey: "k", HTTPClient: server.Client(), DisableCache: true,
	}))

	candidates := []engine.WorkflowCandidate{
		{Name: "test-then-review", Description: "Run tests then review the diff"},
		{Name: "parallel-explore", Description: "Explore several areas at once"},
	}
	got := jev.SuggestWorkflow(context.Background(), "run the tests and then review", candidates)
	if got != "test-then-review" {
		t.Fatalf("got %q, want test-then-review", got)
	}

	// A nil runtime never suggests anything.
	var nilRuntime *jevRuntime
	if got := nilRuntime.SuggestWorkflow(context.Background(), "anything", candidates); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// WorkflowCandidate conversion from loaded definitions must carry name and
// description through untouched.
func TestSuggestWorkflowWithoutJevIsEmpty(t *testing.T) {
	app := &App{WorkflowRegistry: workflow.NewRegistry()}
	if got := app.SuggestWorkflow(context.Background(), "anything"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
