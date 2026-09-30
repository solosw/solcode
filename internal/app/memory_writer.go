package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

// WriteMemory implements tool.MemoryWriter so the model can decide, mid-task,
// that something is worth remembering across sessions. Each WriteMemory call
// is stored as its own entry (duplicates allowed) and tagged with the current
// checkpoint turn when one is open.
func (a *App) WriteMemory(ctx context.Context, req tool.MemoryWriteRequest) (tool.MemoryWriteResult, error) {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return tool.MemoryWriteResult{}, fmt.Errorf("memory is not enabled")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	turn, _ := a.checkpointTurnAndFiles(sessionID, workDir)

	outcome, err := a.MemoryManager.RememberDirect(ctx, memory.DirectInput{
		Text:            req.Text,
		Kind:            memory.Kind(req.Kind),
		Scope:           memory.Scope(req.Scope),
		Tier:            memoryTierForWrite(req.Kind),
		Tags:            req.Tags,
		Importance:      req.Importance,
		Reason:          req.Reason,
		SourceSessionID: sessionID,
		SourceTurn:      turn,
		AllowDuplicate:  true,
	})
	if err != nil {
		return tool.MemoryWriteResult{}, err
	}

	result := tool.MemoryWriteResult{
		Stored: outcome.Stored,
		Merged: outcome.Merged,
		Reason: outcome.Reason,
	}
	if outcome.Stored {
		result.ID = outcome.Item.ID
		result.Text = outcome.Item.Text
		result.Tier = string(outcome.Item.Tier)
		result.Kind = string(outcome.Item.Kind)
		result.Scope = string(outcome.Item.Scope)
		result.Turn = outcome.Item.SourceTurn
	}
	return result, nil
}

// ReadMemory implements tool.MemoryReader so the model can look up what was
// remembered earlier instead of re-deriving it. Cross-session entries stay
// hidden when this session declined cross-session memory.
//
// Empty query returns the current belief set (one active item per topic) plus
// recent archival hits, so the model sees a "settled view" without knowing
// topic keys.
func (a *App) ReadMemory(ctx context.Context, req tool.MemoryReadRequest) (tool.MemoryReadResult, error) {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return tool.MemoryReadResult{}, fmt.Errorf("memory is not enabled")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	allowCrossSession := a.sessionAllowsCrossSessionMemoryByID(ctx, sessionID)

	result := tool.MemoryReadResult{CrossSessionAllowed: allowCrossSession}

	// Belief view: empty query or explicit "beliefs"/"current beliefs".
	q := strings.ToLower(strings.TrimSpace(req.Query))
	wantBeliefs := q == "" || q == "beliefs" || q == "current beliefs" || strings.HasPrefix(q, "topic:")
	if wantBeliefs {
		beliefs, err := a.MemoryManager.CurrentBeliefs(ctx, req.Limit)
		if err != nil {
			return tool.MemoryReadResult{}, err
		}
		if strings.HasPrefix(q, "topic:") {
			topic := strings.TrimSpace(req.Query[len("topic:"):])
			hist, herr := a.MemoryManager.TopicHistory(ctx, topic)
			if herr != nil {
				return tool.MemoryReadResult{}, herr
			}
			if hist.Active != nil && memoryMatchesKindScope(*hist.Active, req.Kind, req.Scope) {
				result.Beliefs = append(result.Beliefs, fmt.Sprintf("[%s] %s", hist.Topic, hist.Active.Text))
			}
			for _, item := range hist.Items {
				if !memoryMatchesKindScope(item, req.Kind, req.Scope) {
					continue
				}
				if len(result.Entries) >= req.Limit {
					break
				}
				result.Entries = append(result.Entries, tool.MemoryEntry{
					Text:         item.Text,
					Tier:         string(item.Tier),
					Kind:         string(item.Kind),
					Scope:        string(item.Scope),
					Tags:         append([]string(nil), item.Tags...),
					Topic:        item.Topic,
					OtherSession: item.SourceSessionID != "" && item.SourceSessionID != sessionID,
				})
			}
			if len(result.Entries) == 0 && len(result.Beliefs) == 0 {
				result.Note = "No memory history for that topic."
			}
			return result, nil
		}
		for _, b := range beliefs {
			if !memoryMatchesKindScope(b.Item, req.Kind, req.Scope) {
				continue
			}
			line := strings.TrimSpace(b.Item.Text)
			if line == "" {
				continue
			}
			prefix := b.Topic
			if prefix != "" {
				line = "[" + prefix + "] " + line
			}
			result.Beliefs = append(result.Beliefs, line)
		}
	}

	// Over-fetch so kind/scope filtering still fills the requested limit.
	fetchLimit := req.Limit
	if req.Kind != "" || req.Scope != "" {
		fetchLimit = req.Limit * 4
	}
	if fetchLimit <= 0 {
		fetchLimit = 8
	}
	items, err := a.MemoryManager.Retrieve(ctx, req.Query, sessionID, allowCrossSession, fetchLimit)
	if err != nil {
		return tool.MemoryReadResult{}, err
	}

	filteredOut := 0
	for _, item := range items {
		if !memoryMatchesKindScope(item, req.Kind, req.Scope) {
			filteredOut++
			continue
		}
		if len(result.Entries) >= req.Limit {
			break
		}
		result.Entries = append(result.Entries, tool.MemoryEntry{
			Text:         item.Text,
			Tier:         string(item.Tier),
			Kind:         string(item.Kind),
			Scope:        string(item.Scope),
			Tags:         append([]string(nil), item.Tags...),
			Topic:        item.Topic,
			OtherSession: item.SourceSessionID != "" && item.SourceSessionID != sessionID,
		})
	}
	// Kind/scope filters that remove every hit should still explain themselves,
	// even when an empty-query belief pass also found nothing after filtering.
	if len(result.Entries) == 0 && filteredOut > 0 {
		result.Note = "Entries existed but none matched the kind/scope filter; retry without it."
		result.Beliefs = nil
	}
	return result, nil
}

func memoryMatchesKindScope(item memory.Item, kind, scope string) bool {
	if kind != "" && string(item.Kind) != kind {
		return false
	}
	if scope != "" && string(item.Scope) != scope {
		return false
	}
	return true
}

// sessionAllowsCrossSessionMemoryByID resolves the stored opt-in flag for a
// session id. Unknown or unreadable sessions are treated as opted out.
func (a *App) sessionAllowsCrossSessionMemoryByID(ctx context.Context, sessionID string) bool {
	if a == nil || a.Sessions == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	current, err := a.Sessions.LoadOrCreate(ctx, session.SessionID(sessionID), a.Config.WorkDir, a.Config.Model)
	if err != nil {
		return false
	}
	return sessionAllowsCrossSessionMemory(current)
}

// memoryTierForWrite picks a starting tier from the declared kind: durable
// knowledge lands in M4, procedural workflows in M5, and task notes stay in M3
// so lifecycle decay can retire them.
func memoryTierForWrite(kind string) memory.Tier {
	switch memory.Kind(strings.ToLower(strings.TrimSpace(kind))) {
	case memory.KindWorkflow:
		return memory.TierProcedural
	case memory.KindTask:
		return memory.TierShortTerm
	default:
		return memory.TierLongTerm
	}
}
