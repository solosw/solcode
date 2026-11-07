package app

import (
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
)

// UpdateModelForNextCall snapshots the next main-model client
// and compression threshold without mutating App.Config or an already running request. Memory
// organization keeps its own backend; SwitchModel reconciles idle app state.
func (a *App) UpdateModelForNextCall(cfg config.Config) {
	next := engine.Config{
		Client:    cpanthropic.NewClient(cpanthropic.Options{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Format: cfg.APIFormat}),
		ModelKey:  cfg.Provider + "\x00" + cfg.APIFormat + "\x00" + cfg.BaseURL + "\x00" + cfg.Model,
		ModelName: cfg.Model, FastModelName: cfg.FastModel, SubagentModelName: cfg.Subagents.Model,
		ModelContextLimit: cfg.ContextLimitForModel,
		MaxTokens:         cfg.MaxTokens, MaxContextTokens: cfg.MaxContextTokens,
		Thinking: cfg.Thinking, ThinkingText: cfg.ThinkingText, Effort: cfg.Effort, Stream: cfg.Stream,
	}
	next.CompactionTriggerPercent = cfg.Memory.CompactionTriggerPercent
	a.Engine.UpdateMainModel(next)
}
