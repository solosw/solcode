package unit_tests

import (
	"encoding/json"
	"testing"

	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestPermissionService_DefaultBlocksDestructiveTools(t *testing.T) {
	service := permission.NewService(permission.ModeDefault)
	decision := service.Check(tool.NewWriteTool(), json.RawMessage(`{"file_path":"x","content":"y"}`))
	if decision.Allowed {
		t.Fatalf("expected destructive tool to be blocked by default")
	}
}

func TestPermissionService_BypassAllowsDestructiveTools(t *testing.T) {
	service := permission.NewService(permission.ModeBypass)
	decision := service.Check(tool.NewWriteTool(), json.RawMessage(`{"file_path":"x","content":"y"}`))
	if !decision.Allowed {
		t.Fatalf("expected bypass mode to allow destructive tool, got %q", decision.Reason)
	}
}

func TestToolToSDKConvertsToolSchema(t *testing.T) {
	converted := cpanthropic.ToolToSDK(tool.NewGrepTool())
	if converted.OfTool == nil {
		t.Fatal("expected custom tool variant")
	}
	if converted.OfTool.Name != "Grep" {
		t.Fatalf("expected Grep tool name, got %q", converted.OfTool.Name)
	}
	if converted.OfTool.InputSchema.Properties == nil {
		t.Fatal("expected input schema properties")
	}
}
