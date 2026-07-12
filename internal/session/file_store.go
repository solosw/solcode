package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileStore struct {
	dir string
}

func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir}
}

func (s *FileStore) Load(ctx context.Context, id SessionID) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := s.pathFor(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NotFoundError{ID: id}
		}
		return nil, fmt.Errorf("read session %q: %w", id, err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse session %q: %w", id, err)
	}
	if session.Metadata.ID == "" {
		session.Metadata.ID = id
	}
	return &session, nil
}

func (s *FileStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	if session.Metadata.ID == "" {
		session.Metadata.ID = "main"
	}
	session.EnsureMessageTimestamps()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	path := s.pathFor(session.Metadata.ID)
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session %q: %w", session.Metadata.ID, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write session %q: %w", session.Metadata.ID, err)
	}
	return nil
}

func (s *FileStore) List(ctx context.Context) ([]Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	metas := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := SessionID(strings.TrimSuffix(entry.Name(), ".json"))
		session, err := s.Load(ctx, id)
		if err != nil {
			continue
		}
		metas = append(metas, session.Metadata)
	}
	sort.SliceStable(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	return metas, nil
}

func (s *FileStore) Delete(ctx context.Context, id SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := s.pathFor(id)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return NotFoundError{ID: id}
		}
		return fmt.Errorf("delete session %q: %w", id, err)
	}
	return nil
}

func (s *FileStore) pathFor(id SessionID) string {
	return filepath.Join(s.dir, sanitizeID(id)+".json")
}

func sanitizeID(id SessionID) string {
	value := strings.TrimSpace(string(id))
	if value == "" {
		return "main"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "main"
	}
	return b.String()
}
