package permission

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/solosw/solcode/internal/tool"
)

type Decision struct {
	Allowed bool
	Reason  string
}

type AskFunc func(toolName string, description string) bool

type Service struct {
	mu              sync.RWMutex
	mode            Mode
	bypassSubagents bool
	allowedTools    map[string]bool
	deniedTools     map[string]bool
	allowedBash     []string
	askFn           AskFunc
}

func NewService(mode Mode) *Service {
	return NewServiceWithConfig(Config{Mode: mode})
}

func NewServiceWithConfig(cfg Config) *Service {
	mode := NormalizeMode(cfg.Mode)
	if mode == "" {
		mode = ModeAuto
	}
	service := &Service{
		mode:            mode,
		bypassSubagents: cfg.BypassSubagents,
		allowedTools:    make(map[string]bool),
		deniedTools:     make(map[string]bool),
		allowedBash:     cleanStringSlice(cfg.AllowBash),
	}
	for _, name := range cleanStringSlice(cfg.Allow) {
		service.allowedTools[name] = true
	}
	return service
}

func (s *Service) Mode() Mode {
	if s == nil {
		return ModeAuto
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// DelegationEnabled reports whether the current mode and settings permit Task.
func (s *Service) DelegationEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode == ModeUltra || (s.mode == ModeBypass && s.bypassSubagents)
}

// SetBypassSubagents applies the setting without resetting session approvals or mode.
func (s *Service) SetBypassSubagents(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bypassSubagents = enabled
}

func (s *Service) SetAskFunc(fn AskFunc) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.askFn = fn
}

// RequestApproval asks the configured interactive approver without applying a
// tool permission policy. It is used for explicit runtime-mode transitions.
func (s *Service) RequestApproval(toolName, description string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	askFn := s.askFn
	s.mu.RUnlock()
	return askFn != nil && askFn(toolName, description)
}

func (s *Service) SetMode(mode Mode) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = NormalizeMode(mode)
}

func (s *Service) AllowTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowedTools[name] = true
	delete(s.deniedTools, name)
}

func (s *Service) DenyTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deniedTools[name] = true
	delete(s.allowedTools, name)
}

func (s *Service) Check(t tool.Tool, input json.RawMessage) Decision {
	if t == nil {
		return Decision{Allowed: false, Reason: "tool is nil"}
	}
	if s == nil {
		return defaultDecision(t, input)
	}

	s.mu.RLock()
	mode := s.mode
	bypassSubagents := s.bypassSubagents
	allowed := s.allowedTools[t.Name()]
	denied := s.deniedTools[t.Name()]
	allowedBash := append([]string(nil), s.allowedBash...)
	askFn := s.askFn
	s.mu.RUnlock()

	// A tool allowlist cannot override the delegation mode/setting gate.
	if t.Name() == tool.TaskToolName || t.Name() == tool.SubagentToolName {
		if mode != ModeUltra && mode != ModeBypass {
			return Decision{Allowed: false, Reason: "Task and sub-agents require bypass (built-in roles) or ultra mode"}
		}
		if mode == ModeBypass {
			if !bypassSubagents {
				return Decision{Allowed: false, Reason: "Task and sub-agents are disabled in bypass; enable permissions.bypass_subagents in settings or use ultra mode"}
			}
			if err := tool.CheckBypassAgentTypes(input); err != nil {
				return Decision{Allowed: false, Reason: err.Error()}
			}
		}
	}
	if denied {
		return Decision{Allowed: false, Reason: fmt.Sprintf("tool denied for session: %s", t.Name())}
	}
	if allowed {
		return Decision{Allowed: true}
	}

	if t.Name() == tool.BashToolName && bashAllowed(input, allowedBash) {
		return Decision{Allowed: true}
	}

	switch mode {
	case ModeBypass, ModeGoal, ModeUltra:
		return Decision{Allowed: true}
	case ModePlan:
		if t.IsReadOnly(input) {
			return Decision{Allowed: true}
		}
		// Planning session may use TodoWrite and ModeSwitch, but not Task.
		if IsPlanModeExtraTool(t.Name()) {
			return Decision{Allowed: true}
		}
		return Decision{Allowed: false, Reason: "plan mode only allows read-only tools, TodoWrite, and ModeSwitch"}
	case ModeAcceptEdits:
		if isAcceptEditsTool(t) {
			return Decision{Allowed: true}
		}
		if askFn != nil && t.IsDestructive(input) {
			description := buildPermissionDescription(t, input)
			if askFn(t.Name(), description) {
				return Decision{Allowed: true}
			}
			return Decision{Allowed: false, Reason: fmt.Sprintf("user denied tool: %s", t.Name())}
		}
		return defaultDecision(t, input)
	case ModeAuto, ModeDefault:
		if t.IsDestructive(input) {
			if askFn != nil {
				description := buildPermissionDescription(t, input)
				if askFn(t.Name(), description) {
					return Decision{Allowed: true}
				}
				return Decision{Allowed: false, Reason: fmt.Sprintf("user denied tool: %s", t.Name())}
			}
		}
		return defaultDecision(t, input)
	default:
		return defaultDecision(t, input)
	}
}

func defaultDecision(t tool.Tool, input json.RawMessage) Decision {
	if t.IsDestructive(input) {
		return Decision{Allowed: false, Reason: fmt.Sprintf("destructive tool requires confirmation: %s", t.Name())}
	}
	return Decision{Allowed: true}
}

func bashAllowed(input json.RawMessage, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return false
	}
	command := strings.TrimSpace(params.Command)
	if command == "" {
		return false
	}
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if command == candidate || strings.HasPrefix(command, candidate+" ") {
			return true
		}
	}
	return false
}

func buildPermissionDescription(t tool.Tool, input json.RawMessage) string {
	inputText := strings.TrimSpace(string(input))
	if len(inputText) > 2_000 {
		inputText = inputText[:2_000] + "..."
	}
	if inputText == "" {
		return fmt.Sprintf("%s wants to run and is marked destructive.", t.Name())
	}
	return fmt.Sprintf("%s wants to run and is marked destructive.\n\nInput:\n%s", t.Name(), inputText)
}

func isAcceptEditsTool(t tool.Tool) bool {
	if t == nil {
		return false
	}
	switch t.Name() {
	case tool.EditToolName, tool.MultiEditToolName, tool.WriteToolName, tool.MultiWriteToolName, tool.PatchToolName, tool.TodoWriteToolName:
		return true
	default:
		return false
	}
}
