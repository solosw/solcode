package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestExecuteDoesNotInvokeAfterCanceledApproval(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		name := "during approval"
		if alreadyCanceled {
			name = "before execution"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if alreadyCanceled {
				cancel()
			}
			invoked, started, captured, asked := false, false, false, false
			reg := tool.NewRegistry()
			reg.Register(fingerprintBashTool{mutate: func(string) error { invoked = true; return nil }})
			permissions := permission.NewService(permission.ModeAuto)
			permissions.SetAskFunc(func(string, string) bool {
				asked = true
				cancel()
				return true // late allow must not start the command
			})
			executor := NewToolExecutorWithPermissions(reg, nil, permissions)
			result := executor.Execute(ctx, ToolCall{Name: tool.BashToolName, Input: json.RawMessage(`{}`)}, ToolEnv{
				UseContext:    &tool.UseContext{WorkDir: t.TempDir(), CaptureCheckpoint: func(string, *string) { captured = true }},
				OnInvokeStart: func() { started = true },
			})
			if !result.IsError || result.Content == nil || !strings.Contains(result.Content.Text, "context canceled") {
				t.Fatalf("cancellation lost: %+v", result)
			}
			if invoked || started || captured || asked == alreadyCanceled {
				t.Fatalf("canceled tool ran callbacks: invoked=%v started=%v captured=%v asked=%v", invoked, started, captured, asked)
			}
		})
	}
}
