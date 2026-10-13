package engine

import (
	"regexp"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
)

// dynamicContext tracks materialized overlays, not transient request fields.
// Updates are appended at model boundaries after all tool results. Earlier
// messages are never moved or rewritten, preserving the conversation prefix.
// Tagged blocks also let a resumed run reuse overlays already in its history.
type dynamicContext struct {
	last map[string]string
}

var dynamicContextClears = map[string]string{
	"skill":        "No skill is automatically selected for this task. Earlier automatic skill selections are superseded.",
	"folded-tools": "Folded-tool catalog updated: no additional tools are currently folded.",
	"working-set":  "Working set reset for the new task. Earlier working-set snapshots are historical, not the current task state.",
}

func newDynamicContext(messages []sdk.MessageParam) *dynamicContext {
	d := &dynamicContext{last: make(map[string]string)}
	for _, message := range messages {
		if !contextmessage.IsDynamicContext(message) {
			continue
		}
		for _, block := range message.Content {
			if block.OfText == nil {
				continue
			}
			kind, body, ok := contextmessage.ParseDynamicContextText(block.OfText.Text)
			if !ok {
				continue
			}
			if clear, exists := dynamicContextClears[kind]; exists && body == clear {
				body = ""
			}
			d.last[kind] = body
		}
	}
	return d
}

// append emits only changed sections. An empty working set / advisory means
// "no update", not "delete the previous snapshot". resetTask explicitly marks
// snapshots from a previous run historical until this run has useful state.
func (d *dynamicContext) append(messages []sdk.MessageParam, b ContextBuilder, req RunRequest, resetTask bool) []sdk.MessageParam {
	if resetTask {
		// Counters belong to a task window. The same warning in a later task
		// is a new event, even if its rendered text matches an older snapshot.
		delete(d.last, "efficiency")
	}
	var blocks []sdk.ContentBlockParamUnion
	add := func(kind, text string, clear bool) {
		text = strings.TrimSpace(text)
		if previous, exists := d.last[kind]; (exists && previous == text) || (!exists && text == "") {
			return
		}
		if text == "" && !clear {
			return
		}
		d.last[kind] = text
		body := text
		if body == "" {
			body = dynamicContextClears[kind]
		}
		blocks = append(blocks, sdk.NewTextBlock(contextmessage.DynamicContextText(kind, body)))
	}
	add("mode", b.ModeInstructions, false)
	skill := strings.TrimSpace(b.ForceSkill)
	if skill != "" {
		skill = forceSkillBlock(skill)
	}
	add("skill", skill, true)
	if knowledge := strings.TrimSpace(req.ProjectKnowledge); knowledge != "" {
		add("project-knowledge", "Project knowledge context:\n"+knowledge, false)
	}
	if summary := sanitizeContextSessionSummary(req.SessionSummary); summary != "" {
		add("session-summary", "Session summary:\n"+summary, false)
	}
	if len(req.MemoryContext) > 0 {
		add("memory", "Retrieved memory:\n"+formatMemoryContext(req.MemoryContext), false)
	}
	add("folded-tools", b.FoldedTools, true)
	add("working-set", b.WorkingSet, resetTask)
	add("efficiency", b.EfficiencyNote, false)
	if len(blocks) > 0 {
		messages = append(messages, sdk.NewUserMessage(blocks...))
	}
	return messages
}

// Efficiency signals are independent of task changes and semantic routing.
// The first new signal is immediate; changing counters are coalesced so a
// stalled agent does not accumulate another advisory on every tool round.
type efficiencyInjection struct {
	lastHint string
	lastKey  string
	lastTurn int
	emitted  bool
}

const efficiencyInjectionCooldown = 4

var efficiencyCounterPattern = regexp.MustCompile(`\b\d+(×| consecutive turns)`)

func (g *efficiencyInjection) next(turn int, hint string) string {
	if hint == "" {
		g.lastHint, g.lastKey = "", ""
		return ""
	}
	// New paths, errors or signal types should not wait behind an older read
	// warning. Only counter-only updates to the same signal need coalescing.
	key := efficiencyCounterPattern.ReplaceAllString(hint, "${1}")
	if hint == g.lastHint || (key == g.lastKey && g.emitted && turn-g.lastTurn < efficiencyInjectionCooldown) {
		return ""
	}
	g.lastHint, g.lastKey, g.lastTurn, g.emitted = hint, key, turn, true
	return hint
}
