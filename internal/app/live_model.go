package app

import (
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
)

// UpdateModelForNextCall does not mutate App.Config or the client being used by
// an in-flight prompt's compaction/memory work. Only the main engine's next
// model call changes; SwitchModel reconciles all ancillary state when idle.
func (a *App) UpdateModelForNextCall(cfg config.Config) {
	a.Engine.UpdateMainModel(engine.Config{
		Client:    cpanthropic.NewClient(cpanthropic.Options{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Format: cfg.APIFormat}),
		ModelKey:  cfg.Provider + "\x00" + cfg.APIFormat + "\x00" + cfg.BaseURL + "\x00" + cfg.Model,
		ModelName: cfg.Model, FastModelName: cfg.FastModel,
		MaxTokens: cfg.MaxTokens, MaxContextTokens: cfg.MaxContextTokens,
		Thinking: cfg.Thinking, ThinkingText: cfg.ThinkingText, Effort: cfg.Effort, Stream: cfg.Stream,
	})
}
