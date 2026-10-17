package engine

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	contextmessage "github.com/solosw/solcode/internal/message"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func (e *Engine) forkSource(name string, messages []sdk.MessageParam, w *WorkingSet) agent.Snapshot {
	if e.config.Tools != nil {
		if selected := e.config.Tools.Find(name); selected != nil {
			name = selected.Name()
		}
	}
	if name != tool.TaskToolName && name != tool.SubagentToolName {
		return agent.Snapshot{}
	}
	// The current assistant tool_use is not a completed parent exchange.
	if len(messages) > 0 {
		messages = messages[:len(messages)-1]
	}
	clean, _ := session.RepairMessages(messages)
	inherited := make([]sdk.MessageParam, 0, len(clean))
	for _, m := range clean {
		if !contextmessage.IsDynamicContext(m) {
			inherited = append(inherited, m)
			continue
		}
		// Preserve useful summaries/knowledge, not the parent's runtime policy.
		var retained []sdk.ContentBlockParamUnion
		for _, b := range m.Content {
			kind, _, _ := contextmessage.ParseDynamicContextText(b.OfText.Text)
			if kind == "session-summary" || kind == "project-knowledge" || kind == "memory" {
				retained = append(retained, b)
			}
		}
		if len(retained) > 0 {
			inherited = append(inherited, sdk.NewUserMessage(retained...))
		}
	}
	return agent.Snapshot{Messages: inherited, WorkingSet: w.Snapshot()}
}
