package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/solosw/solcode/internal/checkpoint"
	"github.com/solosw/solcode/internal/config"
)

// checkpointState holds the active session's file snapshot store.
type checkpointState struct {
	mu    sync.Mutex
	store *checkpoint.Store
	id    string
	dir   string
}

func (a *App) checkpointSessionDir() string {
	if a == nil {
		return ""
	}
	dir := strings.TrimSpace(a.Config.Session.Dir)
	if dir == "" {
		dir = config.DefaultSessionDir(a.Config.WorkDir)
	}
	return dir
}

func (a *App) ensureCheckpointStore(sessionID, workDir string) (*checkpoint.Store, error) {
	if a == nil {
		return nil, fmt.Errorf("app is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	dir := a.checkpointSessionDir()
	if dir == "" {
		return nil, fmt.Errorf("session dir is empty")
	}

	a.ckpt.mu.Lock()
	defer a.ckpt.mu.Unlock()
	if a.ckpt.store != nil && a.ckpt.id == sessionID && a.ckpt.dir == dir {
		return a.ckpt.store, nil
	}
	store, err := checkpoint.NewStore(dir, sessionID, workDir, checkpoint.DefaultRetainTurns)
	if err != nil {
		return nil, err
	}
	a.ckpt.store = store
	a.ckpt.id = sessionID
	a.ckpt.dir = dir
	return store, nil
}

func (a *App) beginCheckpointTurn(sessionID, workDir, prompt string) {
	if a == nil {
		return
	}
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil || store == nil {
		return
	}
	_, _ = store.BeginTurn(prompt)
}

func (a *App) captureCheckpoint(path string, content *string) {
	if a == nil {
		return
	}
	a.ckpt.mu.Lock()
	store := a.ckpt.store
	a.ckpt.mu.Unlock()
	if store == nil {
		return
	}
	_ = store.Capture(path, content)
}

// ListCheckpoints returns code-rewind anchors for a session.
func (a *App) ListCheckpoints(sessionID, workDir string) ([]checkpoint.Meta, error) {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil {
		return nil, err
	}
	return store.List()
}

// NameCheckpoint labels a checkpoint. turn < 0 means the newest checkpoint.
func (a *App) NameCheckpoint(sessionID, workDir string, turn int, name string) (checkpoint.Meta, error) {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil {
		return checkpoint.Meta{}, err
	}
	return store.SetName(turn, name)
}

// RewindCode restores workspace files to the start of the given checkpoint turn.
// Conversation history is not modified.
func (a *App) RewindCode(sessionID, workDir string, turn int) (checkpoint.RestoreResult, error) {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}
	return store.RestoreFiles(turn)
}

// RewindCodeByName restores workspace files for the checkpoint with the given label.
func (a *App) RewindCodeByName(sessionID, workDir, name string) (checkpoint.RestoreResult, int, error) {
	store, err := a.ensureCheckpointStore(sessionID, workDir)
	if err != nil {
		return checkpoint.RestoreResult{}, 0, err
	}
	turn, err := store.FindTurnByName(name)
	if err != nil {
		return checkpoint.RestoreResult{}, 0, err
	}
	result, err := store.RestoreFiles(turn)
	return result, turn, err
}
