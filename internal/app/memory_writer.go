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
func (a *App) ReadMemory(ctx context.Context, req tool.MemoryReadRequest) (tool.MemoryReadResult, error) {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return tool.MemoryReadResult{}, fmt.Errorf("memory is not enabled")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	allowCrossSession := a.sessionAllowsCrossSessionMemoryByID(ctx, sessionID)

	// Over-fetch so kind/scope filtering still fills the requested limit.
	fetchLimit := req.Limit
	if req.Kind != "" || req.Scope != "" {
		fetchLimit = req.Limit * 4
	}
	items, err := a.MemoryManager.Retrieve(ctx, req.Query, sessionID, allowCrossSession, fetchLimit)
	if err != nil {
		return tool.MemoryReadResult{}, err
	}

	result := tool.MemoryReadResult{CrossSessionAllowed: allowCrossSession}
	filteredOut := 0
	for _, item := range items {
		if req.Kind != "" && string(item.Kind) != req.Kind {
			filteredOut++
			continue
		}
		if req.Scope != "" && string(item.Scope) != req.Scope {
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
			OtherSession: item.SourceSessionID != "" && item.SourceSessionID != sessionID,
		})
	}
	if len(result.Entries) == 0 && filteredOut > 0 {
		result.Note = "Entries existed but none matched the kind/scope filter; retry without it."
	}
	return result, nil
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
