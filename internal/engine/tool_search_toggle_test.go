package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestToolSearchDisabledSendsFullStableCatalog(t *testing.T) {
	all := sampleTools()
	// More extras than the dynamic cap, without any lexical or sticky match.
	var first []string
	for _, query := range []string{"", "entirely unrelated", "web search office documents"} {
		selected := SelectToolsForTurn(all, []string{tool.ViewToolName}, query, nil, false)
		names := namesOf(selected)
		for _, candidate := range all {
			name := candidate.Name()
			want := !hiddenFromModel[name] && name != tool.ToolSearchToolName
			if names[name] != want {
				t.Fatalf("query=%q name=%s got=%v want=%v", query, name, names[name], want)
			}
		}
		ordered := make([]string, 0, len(selected))
		for _, entry := range selected {
			ordered = append(ordered, entry.Name())
		}
		if first != nil && !reflect.DeepEqual(first, ordered) {
			t.Fatal("query changed full catalog order")
		}
		first = ordered
	}
}

func TestToolSearchDisabledSkipsRouterAndPreservesExecutorPermissions(t *testing.T) {
	stub := &routerStub{choice: "mcp__office__cli", confidence: 1}
	reg := tool.NewRegistry()
	reg.Register(sampleTools()...)
	client := &persistentClient{respond: func(i int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		names := map[string]bool{}
		for _, entry := range req.Tools {
			if entry.OfTool != nil {
				names[entry.OfTool.Name] = true
			}
		}
		for _, name := range []string{"mcp__office__cli", "mcp__godot__run", "WebSearch", "Fetch"} {
			if !names[name] {
				t.Fatalf("extra %s not directly visible", name)
			}
		}
		for _, name := range []string{tool.ToolSearchToolName, tool.SubagentToolName, tool.WaitToolName, tool.TaskToolName, tool.EditToolName} {
			if names[name] {
				t.Fatalf("unavailable tool %s exposed", name)
			}
		}
		if strings.Contains(req.System, "Call ToolSearch") || !strings.Contains(req.System, "All enabled model-visible tools are provided directly") {
			t.Fatal("wrong tool discovery instructions")
		}
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.OfText != nil && strings.Contains(b.OfText.Text, "kind=\"folded-tools\"") {
					t.Fatal("folded tool hint sent while disabled")
				}
			}
		}
		if i == 0 {
			return backgroundMessage(t, backgroundCall("blocked", "mcp__office__cli", map[string]any{})), nil
		}
		var result string
		for _, text := range backgroundResultTexts(req.Messages, "blocked") {
			result += text
		}
		if !strings.Contains(strings.ToLower(result), "allowed") {
			t.Fatalf("runtime allowlist not enforced: %s", result)
		}
		return backgroundMessage(t), nil
	}}
	eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "chat", MaxTurns: 3, DisableToolSearch: true, Router: stub.router(t), Permissions: permission.NewService(permission.ModeBypass)})
	result := eng.Run(t.Context(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: t.TempDir(), Prompt: "unrelated instruction", AllowedTools: []string{tool.ViewToolName}})
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	if stub.requests != 0 {
		t.Fatalf("tool router called %d times", stub.requests)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	a, _ := json.Marshal(client.requests[0].Tools)
	b, _ := json.Marshal(client.requests[1].Tools)
	if string(a) != string(b) || client.requests[0].System != client.requests[1].System {
		t.Fatal("tool failure changed stable prefix")
	}
}
