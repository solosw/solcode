package unit_tests

import (
	"encoding/json"
	"testing"

	"github.com/solosw/codeplus-agent/internal/permission"
	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestPermissionServiceAskAllowsDestructive(t *testing.T) {
	service := permission.NewService(permission.ModeAuto)
	asked := false
	service.SetAskFunc(func(toolName, description string) bool {
		asked = true
		return true
	})
	decision := service.Check(tool.NewWriteTool(), json.RawMessage(`{"file_path":"x","content":"y"}`))
	if !asked {
		t.Fatal("expected ask callback to be invoked for destructive tool")
	}
	if !decision.Allowed {
		t.Fatalf("expected destructive tool to be allowed after user confirms, got %q", decision.Reason)
	}
}

func TestPermissionServiceAskDeniesWhenUserRefuses(t *testing.T) {
	service := permission.NewService(permission.ModeAuto)
	service.SetAskFunc(func(toolName, description string) bool {
		return false
	})
	decision := service.Check(tool.NewWriteTool(), json.RawMessage(`{"file_path":"x","content":"y"}`))
	if decision.Allowed {
		t.Fatal("expected destructive tool to be denied when user refuses")
	}
}

func TestPermissionServiceReadOnlySkipsAsk(t *testing.T) {
	service := permission.NewService(permission.ModeAuto)
	service.SetAskFunc(func(toolName, description string) bool {
		t.Fatal("ask callback should not be invoked for read-only tools")
		return true
	})
	decision := service.Check(tool.NewGrepTool(), json.RawMessage(`{"pattern":"x"}`))
	if !decision.Allowed {
		t.Fatalf("expected read-only tool to be allowed without ask, got %q", decision.Reason)
	}
}
