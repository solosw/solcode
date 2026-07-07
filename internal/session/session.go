package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type SessionID string

type Metadata struct {
	ID                 SessionID `json:"id"`
	Title              string    `json:"title,omitempty"`
	WorkDir            string    `json:"work_dir,omitempty"`
	Model              string    `json:"model,omitempty"`
	CrossSessionMemory *bool     `json:"cross_session_memory,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Session struct {
	Metadata Metadata           `json:"metadata"`
	Messages []sdk.MessageParam `json:"messages,omitempty"`
	Summary  string             `json:"summary,omitempty"`
}

type Store interface {
	Load(ctx context.Context, id SessionID) (*Session, error)
	Save(ctx context.Context, session *Session) error
	List(ctx context.Context) ([]Metadata, error)
	Delete(ctx context.Context, id SessionID) error
}

type Manager struct {
	store     Store
	defaultID SessionID
}

func NewManager(store Store, defaultID SessionID) *Manager {
	if defaultID == "" {
		defaultID = "main"
	}
	return &Manager{store: store, defaultID: defaultID}
}

func (m *Manager) DefaultID() SessionID {
	if m == nil || m.defaultID == "" {
		return "main"
	}
	return m.defaultID
}

func (m *Manager) LoadOrCreate(ctx context.Context, id SessionID, workDir, model string) (*Session, error) {
	if m == nil || m.store == nil {
		return NewSession(nonEmptyID(id, "main"), workDir, model), nil
	}
	id = nonEmptyID(id, m.DefaultID())
	s, err := m.store.Load(ctx, id)
	if err == nil {
		if workDir != "" {
			s.Metadata.WorkDir = workDir
		}
		if model != "" {
			s.Metadata.Model = model
		}
		return s, nil
	}
	if !IsNotFound(err) {
		return nil, err
	}
	return NewSession(id, workDir, model), nil
}

func (m *Manager) Save(ctx context.Context, s *Session) error {
	if m == nil || m.store == nil || s == nil {
		return nil
	}
	return m.store.Save(ctx, s)
}

func (m *Manager) List(ctx context.Context) ([]Metadata, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.List(ctx)
}

func NewSession(id SessionID, workDir, model string) *Session {
	now := time.Now()
	id = nonEmptyID(id, "main")
	return &Session{
		Metadata: Metadata{
			ID:        id,
			Title:     string(id),
			WorkDir:   workDir,
			Model:     model,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
}

func (s *Session) Append(messages ...sdk.MessageParam) {
	if s == nil || len(messages) == 0 {
		return
	}
	s.Messages = append(s.Messages, messages...)
	s.Metadata.UpdatedAt = time.Now()
}

func (s *Session) ReplaceMessages(messages []sdk.MessageParam) {
	if s == nil {
		return
	}
	s.Messages = append([]sdk.MessageParam(nil), messages...)
	s.Metadata.UpdatedAt = time.Now()
}

func (s *Session) CopyMessages() []sdk.MessageParam {
	if s == nil || len(s.Messages) == 0 {
		return nil
	}
	out := make([]sdk.MessageParam, len(s.Messages))
	copy(out, s.Messages)
	return out
}

type NotFoundError struct {
	ID SessionID
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("session %q not found", e.ID)
}

func IsNotFound(err error) bool {
	_, ok := err.(NotFoundError)
	return ok
}

func nonEmptyID(id SessionID, fallback SessionID) SessionID {
	if strings.TrimSpace(string(id)) != "" {
		return id
	}
	return fallback
}

// Writer and Memory are kept for compatibility with older in-memory callers.
type Writer interface {
	Append(message sdk.MessageParam)
	Messages() []sdk.MessageParam
	Reset()
}

type Memory struct {
	mu       sync.RWMutex
	messages []sdk.MessageParam
}

func NewMemory() *Memory {
	return &Memory{}
}

func (m *Memory) Append(message sdk.MessageParam) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
}

func (m *Memory) Messages() []sdk.MessageParam {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]sdk.MessageParam, len(m.messages))
	copy(out, m.messages)
	return out
}

func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}
