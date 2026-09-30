package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestSwitchModeRequiresPlanAsGatewayAndApproval(t *testing.T) {
	var approvals []string
	application := &App{Permissions: permission.NewService(permission.ModeAuto)}
	application.Permissions.SetAskFunc(func(toolName, description string) bool {
		approvals = append(approvals, toolName+":"+description)
		return true
	})

	if err := application.SwitchMode(context.Background(), "bypass"); err == nil || !strings.Contains(err.Error(), "only be entered from plan") {
		t.Fatalf("auto -> bypass error = %v, want plan gateway error", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("auto -> bypass approvals = %v, want none", approvals)
	}
	if err := application.SwitchMode(context.Background(), "plan"); err != nil {
		t.Fatalf("auto -> plan: %v", err)
	}
	if application.Permissions.Mode() != permission.ModePlan {
		t.Fatalf("mode = %q, want plan", application.Permissions.Mode())
	}
	if err := application.SwitchMode(context.Background(), "goal"); err != nil {
		t.Fatalf("plan -> goal: %v", err)
	}
	if application.Permissions.Mode() != permission.ModeGoal {
		t.Fatalf("mode = %q, want goal", application.Permissions.Mode())
	}
	if len(approvals) != 2 || !strings.HasPrefix(approvals[0], tool.ModeSwitchToolName+":") || !strings.HasPrefix(approvals[1], tool.ModeSwitchToolName+":") {
		t.Fatalf("approvals = %v, want approval for plan and goal", approvals)
	}
}

func TestSwitchModeDoesNotChangeModeWhenApprovalIsDenied(t *testing.T) {
	application := &App{Permissions: permission.NewService(permission.ModeAuto)}
	application.Permissions.SetAskFunc(func(string, string) bool { return false })

	if err := application.SwitchMode(context.Background(), "plan"); err == nil || !strings.Contains(err.Error(), "user denied") {
		t.Fatalf("auto -> plan error = %v, want approval denial", err)
	}
	if application.Permissions.Mode() != permission.ModeAuto {
		t.Fatalf("mode = %q, want auto", application.Permissions.Mode())
	}
}

func TestSwitchModeDeniedPlanExitsDoNotPersist(t *testing.T) {
	for _, target := range []string{"bypass", "goal"} {
		t.Run(target, func(t *testing.T) {
			persisted := 0
			application := &App{
				Permissions: permission.NewService(permission.ModePlan),
				onModeChange: func(permission.Mode) error {
					persisted++
					return nil
				},
			}
			application.Permissions.SetAskFunc(func(string, string) bool { return false })

			if err := application.SwitchMode(context.Background(), target); err == nil || !strings.Contains(err.Error(), "user denied") {
				t.Fatalf("plan -> %s error = %v, want approval denial", target, err)
			}
			if application.Permissions.Mode() != permission.ModePlan {
				t.Fatalf("mode = %q, want plan", application.Permissions.Mode())
			}
			if persisted != 0 {
				t.Fatalf("persistence calls = %d, want 0", persisted)
			}
		})
	}
}

func TestSwitchModeRequiresPlanForEveryNonPlanOrigin(t *testing.T) {
	for _, origin := range []permission.Mode{permission.ModeAuto, permission.ModeAcceptEdits, permission.ModeBypass, permission.ModeGoal} {
		for _, target := range []string{"bypass", "goal"} {
			if string(origin) == target {
				continue
			}
			t.Run(string(origin)+"_to_"+target, func(t *testing.T) {
				approvalCalls := 0
				application := &App{Permissions: permission.NewService(origin)}
				application.Permissions.SetAskFunc(func(string, string) bool {
					approvalCalls++
					return true
				})
				err := application.SwitchMode(context.Background(), target)
				if err == nil || !strings.Contains(err.Error(), "only be entered from plan") {
					t.Fatalf("%s -> %s error = %v, want plan gateway error", origin, target, err)
				}
				if application.Permissions.Mode() != origin {
					t.Fatalf("mode = %q, want %q", application.Permissions.Mode(), origin)
				}
				if approvalCalls != 0 {
					t.Fatalf("approval calls = %d, want 0", approvalCalls)
				}
			})
		}
	}
}

func TestSwitchModePersistsApprovedTransition(t *testing.T) {
	var persisted permission.Mode
	application := &App{
		Permissions: permission.NewService(permission.ModeAuto),
		onModeChange: func(mode permission.Mode) error {
			persisted = mode
			return nil
		},
	}
	application.Permissions.SetAskFunc(func(string, string) bool { return true })

	if err := application.SwitchMode(context.Background(), "plan"); err != nil {
		t.Fatalf("SwitchMode: %v", err)
	}
	if persisted != permission.ModePlan || application.Config.PermissionMode != permission.ModePlan || application.Config.Permissions.Mode != permission.ModePlan {
		t.Fatalf("persisted = %q, config = %#v", persisted, application.Config)
	}
}

type modeSwitchIntegrationClient struct {
	systems []string
}

func (c *modeSwitchIntegrationClient) Create(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	c.systems = append(c.systems, req.System)
	return &sdk.Message{}, nil
}

func TestModeSwitchToolPersistsTransitionAndRefreshesNextEngineRequest(t *testing.T) {
	permissions := permission.NewService(permission.ModeAuto)
	registry := tool.NewRegistry()
	client := &modeSwitchIntegrationClient{}
	cfg := config.Default()
	application := &App{
		Config:      cfg,
		Permissions: permissions,
		Tools:       registry,
		onModeChange: func(mode permission.Mode) error {
			cfg.PermissionMode = mode
			cfg.Permissions.Mode = mode
			return nil
		},
	}
	application.Engine = engine.NewEngine(engine.Config{
		Client:      client,
		Tools:       registry,
		Permissions: permissions,
		ModelName:   "test",
		MaxTurns:    1,
	})
	registry.Register(tool.NewModeSwitchTool(application.SwitchMode))
	permissions.SetAskFunc(func(string, string) bool { return true })

	first := application.Engine.Run(context.Background(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "first", MaxTurns: 1})
	if first.Error != "" {
		t.Fatalf("first engine run: %s", first.Error)
	}
	modeTool := registry.Find(tool.ModeSwitchToolName)
	result, err := modeTool.Invoke(context.Background(), nil, json.RawMessage(`{"mode":"plan"}`))
	if err != nil {
		t.Fatalf("ModeSwitch Invoke: %v", err)
	}
	if result.IsError {
		t.Fatalf("ModeSwitch result = %#v", result)
	}
	if application.Config.PermissionMode != permission.ModePlan || application.Config.Permissions.Mode != permission.ModePlan {
		t.Fatalf("persisted config = %#v, want plan", application.Config)
	}

	second := application.Engine.Run(context.Background(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "second", MaxTurns: 1})
	if second.Error != "" {
		t.Fatalf("second engine run: %s", second.Error)
	}
	if len(client.systems) != 2 {
		t.Fatalf("engine requests = %d, want 2", len(client.systems))
	}
	// 简短规则常驻 system prompt：两次请求都应包含，且完全一致。
	if !strings.Contains(client.systems[0], permission.PlanModeShortInstructions) {
		t.Fatalf("first request missing compact plan rules: %s", client.systems[0])
	}
	if !strings.Contains(client.systems[1], permission.PlanModeShortInstructions) {
		t.Fatalf("second request missing compact plan rules: %s", client.systems[1])
	}
	if client.systems[0] != client.systems[1] {
		t.Fatalf("system prefix changed across mode switch\nfirst:\n%s\nsecond:\n%s", client.systems[0], client.systems[1])
	}
	// 详细 ACTIVE 指令只在动态上下文中出现，不进 system prompt。
	if strings.Contains(client.systems[1], "(ACTIVE)") {
		t.Fatalf("active plan instructions leaked into system prefix: %s", client.systems[1])
	}
}
