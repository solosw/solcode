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
// the files changed during this session, the todolist snapshot (with Jev
// judgments when enabled), the timestamp and the session id.
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
	task := sessionTaskContext(req.Summary, "")
	files = a.pruneSessionFiles(ctx, task, files)
	todos := a.judgeCurrentTodos(ctx, workDir, task)
	now := time.Now()

	entry, err := store.Append(ctx, sessionmemory.Entry{
		Keywords:   req.Keywords,
		Summary:    req.Summary,
		Importance: req.Importance,
		Turn:       turn,
		Files:      files,
		Todos:      todos,
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
// Results are scoped to the current session id only — never other sessions.
// When several turns recorded todolist snapshots, the newest item per id is
// merged into each returned entry's display via the sessionmemory store; the
// raw per-turn todos stay on the stored entries.
func (a *App) ReadSessionMemory(ctx context.Context, req tool.SessionMemoryReadRequest) (tool.SessionMemoryReadResult, error) {
	store := a.sessionMemoryStore(req.WorkDir)
	if store == nil {
		return tool.SessionMemoryReadResult{}, fmt.Errorf("work directory is unknown")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(a.Config.Session.DefaultSession)
	}
	if sessionID == "" {
		sessionID = "main"
	}
	entries, err := store.ReadForSession(ctx, sessionID, req.Query, req.Limit)
	if err != nil {
		return tool.SessionMemoryReadResult{}, err
	}
	// Merge todos across the whole session so the caller sees the latest view.
	all, _ := store.ReadForSession(ctx, sessionID, "", 100)
	mergedTodos := sessionmemory.MergeTodos(all)

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
			Todos:      formatMergedTodos(mergedTodos),
		})
	}
	return result, nil
}

// recordTurnSessionMemory writes one per-turn session memory after a main
// agent turn. It captures the current todolist (Jev-judged when enabled) and
// the pruned set of changed files. Failures are logged and ignored so a turn
// never fails because session memory could not be written.
func (a *App) recordTurnSessionMemory(ctx context.Context, sessionID, workDir, prompt, summary string) {
	if a == nil {
		return
	}
	store := a.sessionMemoryStore(workDir)
	if store == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	turn, files := a.checkpointTurnAndFiles(sessionID, workDir)
	task := sessionTaskContext(prompt, summary)
	files = a.pruneSessionFiles(ctx, task, files)
	todos := a.judgeCurrentTodos(ctx, workDir, task)
	if len(todos) == 0 && len(files) == 0 && strings.TrimSpace(prompt) == "" {
		return
	}
	summaryText := strings.TrimSpace(prompt)
	if summaryText == "" {
		summaryText = strings.TrimSpace(summary)
	}
	if summaryText == "" {
		summaryText = "Turn completed."
	} else if len([]rune(summaryText)) > 400 {
		summaryText = string([]rune(summaryText)[:400]) + "…"
	}
	summaryText = "Turn memory: " + summaryText
	_, err := store.Append(ctx, sessionmemory.Entry{
		Keywords:   []string{"turn", "todolist"},
		Summary:    summaryText,
		Importance: 0.4,
		Turn:       turn,
		Files:      files,
		Todos:      todos,
		Time:       time.Now(),
		SessionID:  sessionID,
	})
	if err != nil {
		jevLog("session turn memory not stored: " + err.Error())
	}
}

// recordTodoSessionMemory writes a todolist snapshot after each successful
// TodoWrite. Mid-turn updates are recorded separately from the turn-end entry
// so a conversation with many TodoWrite calls keeps every intermediate state.
// Failures are logged and ignored.
func (a *App) recordTodoSessionMemory(ctx context.Context, sessionID, workDir string, todos []tool.TodoItem) {
	if a == nil {
		return
	}
	store := a.sessionMemoryStore(workDir)
	if store == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	turn, _ := a.checkpointTurnAndFiles(sessionID, workDir)
	task := "Todolist update"
	if active := storeActiveTurnPrompt(a, sessionID, workDir); active != "" {
		task = active
	}
	judged := a.judgeTodos(ctx, task, todos)
	if len(judged) == 0 {
		// Still record an empty snapshot when the model cleared the list
		// (all items completed) so MergeTodos can see the cleared state.
		judged = nil
	}
	summaryText := fmt.Sprintf("Todolist update (%d items).", len(todos))
	_, err := store.Append(ctx, sessionmemory.Entry{
		Keywords:   []string{"todolist", "todo-write"},
		Summary:    summaryText,
		Importance: 0.35,
		Turn:       turn,
		Todos:      judged,
		Time:       time.Now(),
		SessionID:  sessionID,
	})
	if err != nil {
		jevLog("session todolist memory not stored: " + err.Error())
	}
}

func storeActiveTurnPrompt(a *App, sessionID, workDir string) string {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil || store == nil {
		return ""
	}
	turn, _, active := store.ActiveTurnInfo()
	if !active {
		return ""
	}
	cp, err := store.Load(turn)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cp.Prompt)
}

func (a *App) judgeCurrentTodos(ctx context.Context, workDir, task string) []sessionmemory.TodoJudgment {
	return a.judgeTodos(ctx, task, loadSessionTodos(workDir))
}

func (a *App) judgeTodos(ctx context.Context, task string, todos []tool.TodoItem) []sessionmemory.TodoJudgment {
	if a == nil || a.jev == nil {
		return snapshotTodosWithoutJev(todos)
	}
	return a.jev.judgeSessionTodos(ctx, task, todos)
}

func (a *App) pruneSessionFiles(ctx context.Context, task string, files []string) []string {
	files = filterUnimportantSessionFiles(files)
	if a == nil || a.jev == nil {
		return files
	}
	return a.jev.screenSessionFiles(ctx, task, files)
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

func formatMergedTodos(todos []sessionmemory.TodoJudgment) []string {
	if len(todos) == 0 {
		return nil
	}
	out := make([]string, 0, len(todos))
	for _, todo := range todos {
		mark := "[ ]"
		switch {
		case todo.Done:
			mark = "[✓]"
		case todo.Status == sessionmemory.TodoInProgress:
			mark = "[→]"
		}
		valid := ""
		if !todo.Valid {
			valid = " (invalid)"
		}
		label := strings.TrimSpace(todo.Content)
		if label == "" {
			label = strings.TrimSpace(todo.ID)
		}
		out = append(out, fmt.Sprintf("%s %s%s", mark, label, valid))
	}
	return out
}
