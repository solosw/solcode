// Package agentsession adapts the existing session storage to agent.Store.
package agentsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/session"
)

type FileStore struct {
	dir          string
	sessions     *session.FileStore
	forks        *session.FileStore
	forkMessages *session.FileStore
}

func NewFileStore(sessionDir string) *FileStore {
	dir := filepath.Join(sessionDir, "subagents")
	return &FileStore{dir: dir, sessions: session.NewFileStore(dir), forks: session.NewFileStore(filepath.Join(dir, "forks")), forkMessages: session.NewFileStore(filepath.Join(dir, "forks", "messages"))}
}

func (s *FileStore) Load(ctx context.Context, id agent.AgentID) (*agent.Record, error) {
	if !agent.ValidID(string(id)) {
		return nil, fmt.Errorf("invalid agent id")
	}
	saved, err := s.sessions.Load(ctx, session.SessionID(id))
	if session.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s", agent.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var r agent.Record
	if err = json.Unmarshal(saved.Subagent, &r); err != nil {
		return nil, fmt.Errorf("load agent metadata: %w", err)
	}
	if r.Status.ID != id || r.Config.ID != id {
		return nil, fmt.Errorf("agent metadata id mismatch")
	}
	r.Messages = saved.Messages
	r.MessageTimestamps = saved.MessageTimestamps
	return &r, nil
}

func (s *FileStore) Save(ctx context.Context, r *agent.Record) error {
	if !agent.ValidID(string(r.Config.ID)) {
		return fmt.Errorf("invalid agent id")
	}
	meta := *r
	meta.Messages = nil
	meta.MessageTimestamps = nil
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	saved := session.NewSession(session.SessionID(r.Config.ID), r.Config.WorkDir, r.Config.Model)
	saved.Metadata.Title = r.Config.Description
	saved.Metadata.CreatedAt = r.CreatedAt
	saved.Metadata.UpdatedAt = r.UpdatedAt
	saved.Subagent = data
	saved.Messages = r.Messages
	saved.MessageTimestamps = r.MessageTimestamps
	for _, run := range r.Runs {
		saved.Metadata.Usage.Add(run.Usage.InputTokens, run.Usage.OutputTokens, run.Usage.CacheCreationInputTokens, run.Usage.CacheReadInputTokens)
	}
	return s.sessions.Save(ctx, saved)
}

func (s *FileStore) List(ctx context.Context) ([]*agent.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []*agent.Record
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		r, err := s.Load(ctx, agent.AgentID(strings.TrimSuffix(entry.Name(), ".json")))
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

func (s *FileStore) Acquire(ctx context.Context, id agent.AgentID) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !agent.ValidID(string(id)) {
		return nil, fmt.Errorf("invalid agent id")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	// Never unlink a lock file: another process may hold its inode. OS locks
	// disappear on process death, so a crash cannot leave a permanent busy flag.
	file, err := os.OpenFile(filepath.Join(s.dir, string(id)+".run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s (%v)", agent.ErrBusy, id, err)
	}
	var once sync.Once
	var releaseErr error
	return func() error { once.Do(func() { releaseErr = file.Close() }); return releaseErr }, nil
}

type forkManifest struct {
	Version    int             `json:"version"`
	Messages   []string        `json:"messages,omitempty"`
	WorkingSet json.RawMessage `json:"working_set,omitempty"`
}

func (s *FileStore) SaveFork(ctx context.Context, snapshot agent.Snapshot) (string, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	id := "fork-" + hex.EncodeToString(sum[:])
	if _, err := s.forks.Load(ctx, session.SessionID(id)); err == nil {
		return id, nil
	} else if !session.IsNotFound(err) {
		return "", err
	}
	// Store individual immutable messages, so snapshots taken at different
	// parent turns share their common prefix instead of duplicating it on disk.
	manifest := forkManifest{Version: 1, WorkingSet: snapshot.WorkingSet}
	for _, message := range snapshot.Messages {
		data, err := json.Marshal(message)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		messageID := session.SessionID("message-" + hex.EncodeToString(sum[:]))
		if _, err := s.forkMessages.Load(ctx, messageID); session.IsNotFound(err) {
			content := session.NewSession(messageID, "", "")
			content.Messages = []sdk.MessageParam{message}
			if err := s.forkMessages.Save(ctx, content); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
		manifest.Messages = append(manifest.Messages, string(messageID))
	}
	saved := session.NewSession(session.SessionID(id), "", "")
	saved.Subagent, err = json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return id, s.forks.Save(ctx, saved)
}

func (s *FileStore) LoadFork(ctx context.Context, id string) (agent.Snapshot, error) {
	if !agent.ValidID(id) {
		return agent.Snapshot{}, fmt.Errorf("invalid fork id")
	}
	saved, err := s.forks.Load(ctx, session.SessionID(id))
	if err != nil {
		return agent.Snapshot{}, err
	}
	var manifest forkManifest
	if err := json.Unmarshal(saved.Subagent, &manifest); err != nil && len(saved.Subagent) > 0 {
		return agent.Snapshot{}, err
	}
	if manifest.Version == 0 {
		return agent.Snapshot{Messages: saved.Messages, WorkingSet: saved.Subagent}, nil
	}
	if manifest.Version != 1 {
		return agent.Snapshot{}, fmt.Errorf("unsupported fork snapshot version %d", manifest.Version)
	}
	snapshot := agent.Snapshot{WorkingSet: manifest.WorkingSet}
	for _, messageID := range manifest.Messages {
		if !agent.ValidID(messageID) {
			return agent.Snapshot{}, fmt.Errorf("invalid fork message reference")
		}
		content, err := s.forkMessages.Load(ctx, session.SessionID(messageID))
		if err != nil {
			return agent.Snapshot{}, err
		}
		if len(content.Messages) != 1 {
			return agent.Snapshot{}, fmt.Errorf("invalid fork message snapshot %s", messageID)
		}
		snapshot.Messages = append(snapshot.Messages, content.Messages[0])
	}
	return snapshot, nil
}
