package engine

import (
	"encoding/json"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
	"strings"
	"testing"
)

func TestPersistentChildRestoresCompressionEvidence(t *testing.T) {
	dir := t.TempDir()
	workdir := t.TempDir()
	reg := tool.NewRegistry()
	reg.Register(tool.ContextTools(nil, nil)...)
	state := contextcompression.New()
	original := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("old request")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("PRIVATE_ORIGINAL_EVIDENCE ", 300))), sdk.NewUserMessage(sdk.NewTextBlock("new task")), sdk.NewAssistantMessage(sdk.NewTextBlock("ready"))}
	if err := state.Sync(original); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Compress(contextcompression.Request{Start: "m1", End: "m2", Summary: "Finished old task."}); err != nil {
		t.Fatal(err)
	}
	view, _ := state.Messages()
	cfg := persistentConfig(workdir)
	cfg.ParentContext = agent.Snapshot{Messages: view, Compression: state}
	cfg.MaxTurns = 3
	first := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) { return backgroundMessage(t), nil }}
	c := agent.NewCoordinator(NewEngine(Config{Client: first, Tools: reg, ModelName: "chat", Permissions: permission.NewService(permission.ModeBypass)}), agentsession.NewFileStore(dir))
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if result := persistentWait(t, c, cfg.ID); result.Error != "" {
		t.Fatal(result.Error)
	}
	c.Close()
	// A new coordinator and engine simulate reopening the persisted child.
	second := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if index == 0 {
			return backgroundMessage(t, backgroundCall("retrieve", "decompress", map[string]any{"block_id": "m2"})), nil
		}
		texts := backgroundResultTexts(req.Messages, "retrieve")
		if len(texts) != 1 || !strings.Contains(texts[0], "PRIVATE_ORIGINAL_EVIDENCE") {
			t.Fatalf("archive unavailable on resume: %v", texts)
		}
		return backgroundMessage(t), nil
	}}
	resumed := agent.NewCoordinator(NewEngine(Config{Client: second, Tools: reg, ModelName: "chat", Permissions: permission.NewService(permission.ModeBypass)}), agentsession.NewFileStore(dir))
	defer resumed.Close()
	cfg.Prompt = "read earlier evidence"
	cfg.ParentContext = agent.Snapshot{}
	if _, err := resumed.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if result := persistentWait(t, resumed, cfg.ID); result.Error != "" {
		t.Fatal(result.Error)
	}
	record, err := resumed.Get(t.Context(), cfg.ID, persistentScope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(record.Compression)
	if !strings.Contains(string(b), "PRIVATE_ORIGINAL_EVIDENCE") {
		t.Fatal("evidence not persisted")
	}
}
