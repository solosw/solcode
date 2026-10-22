package acp

import (
	"net/url"
	"strings"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
)

// SessionConfigOption is ACP's select configuration option. IDs and values are
// opaque to clients; category determines the selector's semantic purpose.
type SessionConfigOption struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description,omitempty"`
	Category     string                     `json:"category"`
	Type         string                     `json:"type"`
	CurrentValue string                     `json:"currentValue"`
	Options      []SessionConfigOptionValue `json:"options"`
}

type SessionConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionSetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Type      string `json:"type,omitempty"`
	Value     string `json:"value"`
}

type SessionSetConfigOptionResult struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

type configModelChoice struct {
	SessionConfigOptionValue
	provider string
	model    string
}

// The configuration ID is opaque; thought_level is the ACP UI category, not
// the ID. Match codex-acp while accepting our previously published ID below.
const reasoningEffortConfigID = "reasoning_effort"
const legacyThoughtLevelConfigID = "thought_level"

// Qualify model values by provider, since names/IDs can overlap across providers.
func configModelValue(provider, model string) string {
	if provider == "" {
		return model
	}
	return url.QueryEscape(provider) + "/" + url.QueryEscape(model)
}

func configModelChoices(cfg config.Config) ([]configModelChoice, string) {
	var choices []configModelChoice
	current := ""
	seen := make(map[string]bool)
	for _, provider := range cfg.Providers {
		for _, model := range provider.Models {
			name := model.Name
			if name == "" {
				name = model.ID
			}
			if name == "" {
				continue
			}
			value := configModelValue(provider.Name, name)
			if seen[value] {
				continue
			}
			seen[value] = true
			label := model.DisplayName
			if label == "" {
				label = name
			}
			choices = append(choices, configModelChoice{
				SessionConfigOptionValue: SessionConfigOptionValue{Value: value, Name: provider.Name + " / " + label},
				provider:                 provider.Name, model: name,
			})
			if current == "" && (cfg.Provider == provider.Name || cfg.Provider == "") && (cfg.Model == name || cfg.Model == model.ID) {
				current = value
			}
		}
	}
	// Legacy single-model settings and unlisted active model IDs remain selectable.
	if current == "" {
		current = configModelValue(cfg.Provider, cfg.Model)
		if !seen[current] {
			choices = append(choices, configModelChoice{
				SessionConfigOptionValue: SessionConfigOptionValue{Value: current, Name: cfg.Model},
				provider:                 cfg.Provider, model: cfg.Model,
			})
		}
	}
	return choices, current
}

func sessionConfigOptions(cfg config.Config) []SessionConfigOption {
	modelChoices, currentModel := configModelChoices(cfg)
	model := SessionConfigOption{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: currentModel}
	for _, choice := range modelChoices {
		model.Options = append(model.Options, choice.SessionConfigOptionValue)
	}
	mode := SessionConfigOption{ID: "mode", Name: "Agent Mode", Category: "mode", Type: "select", CurrentValue: string(permission.NormalizeMode(cfg.PermissionMode))}
	for _, info := range permission.AvailableModes() {
		mode.Options = append(mode.Options, SessionConfigOptionValue{Value: string(info.ID), Name: info.Name, Description: info.Description})
	}
	options := []SessionConfigOption{mode, model}
	if levels := cfg.ModelEfforts(); len(levels) > 0 {
		thought := SessionConfigOption{ID: reasoningEffortConfigID, Name: "Reasoning effort", Description: "How much reasoning effort the model should use", Category: "thought_level", Type: "select", CurrentValue: cfg.Effort}
		for _, effort := range levels {
			label := strings.ToUpper(effort[:1]) + effort[1:]
			if effort == "xhigh" {
				label = "XHigh"
			}
			thought.Options = append(thought.Options, SessionConfigOptionValue{Value: effort, Name: label})
		}
		options = append(options, thought)
	}
	return options
}

func configOptionHasValue(option SessionConfigOption, value string) bool {
	for _, candidate := range option.Options {
		if candidate.Value == value {
			return true
		}
	}
	return false
}

func (sess *acpSession) configOptions() []SessionConfigOption {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sessionConfigOptions(sess.cfg)
}

func (s *Server) emitConfigOptions(sess *acpSession) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: sessionConfigOptions(sess.cfg)})
}

func (s *Server) handleSessionSetConfigOption(msg jsonrpcMessage) error {
	if err := s.requireInitialized(); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, err.Error())
	}
	var params SessionSetConfigOptionParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess == nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "unknown session")
	}
	if params.Type != "" && params.Type != "select" {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "only select configuration options are supported")
	}
	// Serialize commit, notification and response with autonomous changes, so
	// an older response cannot follow a newer committed configuration snapshot.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	options, err := sess.setConfigOptionLocked(params.ConfigID, params.Value)
	if err != nil {
		return s.conn.ReplyError(msg.ID, configErrorCode(err), err.Error())
	}
	if params.ConfigID == "mode" {
		s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: string(sess.application.Permissions.Mode())})
	}
	s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: options})
	return s.conn.Reply(msg.ID, SessionSetConfigOptionResult{ConfigOptions: options})
}

func (sess *acpSession) setConfigOption(id, value string) ([]SessionConfigOption, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.setConfigOptionLocked(id, value)
}

func (sess *acpSession) setConfigOptionLocked(id, value string) ([]SessionConfigOption, error) {
	// Normalize before validation so both IDs share capability checks, no-op
	// handling and the same atomic commit path. Never publish duplicate controls.
	if id == legacyThoughtLevelConfigID {
		id = reasoningEffortConfigID
	}
	options := sessionConfigOptions(sess.cfg)
	valid := false
	for _, option := range options {
		if option.ID == id {
			valid = configOptionHasValue(option, value)
			if valid && option.CurrentValue == value {
				return options, nil // Reselecting a model must not reset explicit effort.
			}
			break
		}
	}
	if !valid {
		return nil, invalidConfigError("unknown configuration option or invalid value: %q = %q", id, value)
	}
	next := sess.cfg
	switch id {
	case "model":
		choices, _ := configModelChoices(next)
		for _, choice := range choices {
			if choice.Value == value {
				var err error
				next, err = selectSessionModel(next, choice.provider, choice.model)
				if err != nil {
					return nil, err
				}
				break
			}
		}
	case "mode":
		next.PermissionMode = permission.Mode(value)
		next.Permissions.Mode = next.PermissionMode
	case reasoningEffortConfigID:
		next.Effort = value
	}
	if err := sess.commitConfigLocked(next); err != nil {
		return nil, err
	}
	return sessionConfigOptions(next), nil
}

func (sess *acpSession) applyPendingConfig() error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.pendingConfig || sess.prompting {
		return nil
	}
	if err := sess.application.SwitchModel(sess.cfg); err != nil {
		return err
	}
	sess.pendingConfig = false
	return nil
}
