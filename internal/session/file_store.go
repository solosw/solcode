package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type FileStore struct {
	dir string
}

type sessionLock struct {
	path  string
	owner string
}

func (l *sessionLock) Release() error {
	if l == nil || l.path == "" || l.owner == "" {
		return nil
	}
	if err := os.Remove(l.owner); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		// Other terminals may still own this session.
		return nil
	}
	return nil
}

func (s *FileStore) Acquire(ctx context.Context, id SessionID) (func() error, error) {
	return s.acquire(ctx, id, false)
}

func (s *FileStore) AcquireIgnoringExisting(ctx context.Context, id SessionID) (func() error, error) {
	return s.acquire(ctx, id, true)
}

func (s *FileStore) acquire(ctx context.Context, id SessionID, ignoreExisting bool) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	path := s.pathFor(id) + ".lock"
	if ignoreExisting {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("lock session %q: %w", id, err)
		}
	} else if err := os.Mkdir(path, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("session %q is already open in another solcode window", id)
		}
		return nil, fmt.Errorf("lock session %q: %w", id, err)
	}
	owner, err := os.CreateTemp(path, "owner-")
	if err != nil {
		if !ignoreExisting {
			_ = os.Remove(path)
		}
		return nil, fmt.Errorf("record session %q owner: %w", id, err)
	}
	ownerPath := owner.Name()
	if err := owner.Close(); err != nil {
		_ = os.Remove(ownerPath)
		if !ignoreExisting {
			_ = os.Remove(path)
		}
		return nil, fmt.Errorf("record session %q owner: %w", id, err)
	}
	return (&sessionLock{path: path, owner: ownerPath}).Release, nil
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
	// Replace only after a complete, synced write. Readers see the old or new
	// snapshot, never truncated JSON. The caller owns the session's writer lock.
	temp, err := os.CreateTemp(s.dir, ".session-*")
	if err != nil {
		return fmt.Errorf("create session temporary file: %w", err)
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return fmt.Errorf("write session %q: %w", session.Metadata.ID, err)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("replace session %q: %w", session.Metadata.ID, err)
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(s.dir)
		if err != nil {
			return err
		}
		defer dir.Close()
		if err := dir.Sync(); err != nil {
			return fmt.Errorf("sync sessions directory: %w", err)
		}
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
