package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/sessionmemory"
	"github.com/solosw/solcode/internal/tool"
)

// sessionMemoryStore returns the project solcode.md store, or nil when no
// working directory is known.
func (a *App) sessionMemoryStore(workDir string) *sessionmemory.Store {
	if a == nil {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	if strings.TrimSpace(workDir) == "" {
		return nil
	}
	return sessionmemory.NewStore(workDir)
}

// WriteSessionMemory implements tool.SessionMemoryWriter: the model supplies
// keywords, summary and importance; the runtime attaches the checkpoint turn,
// the files changed during this session, the timestamp and the session id.
func (a *App) WriteSessionMemory(ctx context.Context, req tool.SessionMemoryWriteRequest) (tool.SessionMemoryWriteResult, error) {
	store := a.sessionMemoryStore(req.WorkDir)
	if store == nil {
		return tool.SessionMemoryWriteResult{}, fmt.Errorf("work directory is unknown")
	}
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	if sessionID == "" {
		sessionID = "main"
	}

	turn, files := a.checkpointTurnAndFiles(sessionID, workDir)
	now := time.Now()

	entry, err := store.Append(ctx, sessionmemory.Entry{
		Keywords:   req.Keywords,
		Summary:    req.Summary,
		Importance: req.Importance,
		Turn:       turn,
		Files:      files,
		Time:       now,
		SessionID:  sessionID,
	})
	if err != nil {
		return tool.SessionMemoryWriteResult{Reason: err.Error()}, nil
	}
	return tool.SessionMemoryWriteResult{
		Stored:     true,
		Turn:       entry.Turn,
		Files:      entry.Files,
		Time:       entry.Time.Format("2006-01-02 15:04:05"),
		SessionID:  entry.SessionID,
		Importance: entry.Importance,
		Path:       store.Path(),
	}, nil
}

// ReadSessionMemory implements tool.SessionMemoryReader.
func (a *App) ReadSessionMemory(ctx context.Context, req tool.SessionMemoryReadRequest) (tool.SessionMemoryReadResult, error) {
	store := a.sessionMemoryStore(req.WorkDir)
	if store == nil {
		return tool.SessionMemoryReadResult{}, fmt.Errorf("work directory is unknown")
	}
	entries, err := store.Read(ctx, req.Query, req.Limit)
	if err != nil {
		return tool.SessionMemoryReadResult{}, err
	}
	result := tool.SessionMemoryReadResult{Path: store.Path()}
	for _, entry := range entries {
		result.Entries = append(result.Entries, tool.SessionMemoryEntry{
			Keywords:   entry.Keywords,
			Summary:    entry.Summary,
			Importance: entry.Importance,
			Turn:       entry.Turn,
			Files:      entry.Files,
			Time:       entry.Time.Format("2006-01-02 15:04:05"),
			SessionID:  entry.SessionID,
		})
	}
	return result, nil
}

// checkpointTurnAndFiles resolves the turn number and changed-file list recorded
// with a session memory. The turn is the in-progress turn when one is open,
// otherwise the newest checkpoint turn; the file list is the union of every file
// the checkpoint captured across the session.
func (a *App) checkpointTurnAndFiles(sessionID, workDir string) (int, []string) {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil || store == nil {
		return -1, nil
	}
	turn, files, active := store.ActiveTurnInfo()
	if !active {
		if latest, ok := store.LatestTurn(); ok {
			turn = latest
			files = store.TurnFiles(latest)
		} else {
			turn = -1
		}
	}
	if all := store.FilesAllTurns(); len(all) > 0 {
		files = all
	}
	return turn, files
}