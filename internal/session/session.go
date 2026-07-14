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
	ID                           SessionID `json:"id"`
	Title                        string    `json:"title,omitempty"`
	WorkDir                      string    `json:"work_dir,omitempty"`
	Model                        string    `json:"model,omitempty"`
	CrossSessionMemory           *bool     `json:"cross_session_memory,omitempty"`
	MemoryBootstrapPending       bool      `json:"memory_bootstrap_pending,omitempty"`
	MemorySummaryCompleted       bool      `json:"memory_summary_completed,omitempty"`
	MemoryCompactionCompleted    bool      `json:"memory_compaction_completed,omitempty"`
	MemoryCompactionMessageCount int       `json:"memory_compaction_message_count,omitempty"`
	CreatedAt                    time.Time `json:"created_at"`
	UpdatedAt                    time.Time `json:"updated_at"`
}

const (
	CompactedSummaryPrefix          = "Compacted session summary:\n"
	CompactedProjectKnowledgePrefix = "Compacted project knowledge:\n"
)

type Session struct {
	Metadata          Metadata           `json:"metadata"`
	Messages          []sdk.MessageParam `json:"messages,omitempty"`
	MessageTimestamps []time.Time        `json:"message_timestamps,omitempty"`
	Summary           string             `json:"summary,omitempty"`
}

// NewCompactedSummaryMessage creates the durable user message that represents
// history removed by compaction. The summary is also stored in Session.Summary
// for direct access and backwards-compatible session files.
func NewCompactedSummaryMessage(summary string) sdk.MessageParam {
	return sdk.NewUserMessage(sdk.NewTextBlock(CompactedSummaryPrefix + strings.TrimSpace(summary)))
}

// NewCompactedProjectKnowledgeMessage creates the durable project knowledge
// message captured at the same time as a session compaction.
func NewCompactedProjectKnowledgeMessage(knowledge string) sdk.MessageParam {
	return sdk.NewUserMessage(sdk.NewTextBlock(CompactedProjectKnowledgePrefix + strings.TrimSpace(knowledge)))
}

// IsCompactedSummaryMessage identifies the durable summary message created by
// NewCompactedSummaryMessage. It must not be treated as transient context.
func IsCompactedSummaryMessage(message sdk.MessageParam) bool {
	return isCompactedContextMessage(message, CompactedSummaryPrefix)
}

func IsCompactedProjectKnowledgeMessage(message sdk.MessageParam) bool {
	return isCompactedContextMessage(message, CompactedProjectKnowledgePrefix)
}

func isCompactedContextMessage(message sdk.MessageParam, prefix string) bool {
	if string(message.Role) != "user" {
		return false
	}
	for _, block := range message.Content {
		if block.OfText != nil && strings.HasPrefix(strings.TrimSpace(block.OfText.Text), prefix) {
			return true
		}
	}
	return false
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
	s, _, err := m.LoadOrCreateWithStatus(ctx, id, workDir, model)
	return s, err
}

// LoadOrCreateWithStatus returns whether the session was created for this
// request. Callers use this to distinguish a new-session bootstrap from a
// normal continuation without inferring it from an empty message list.
func (m *Manager) LoadOrCreateWithStatus(ctx context.Context, id SessionID, workDir, model string) (*Session, bool, error) {
	if m == nil || m.store == nil {
		return NewSession(nonEmptyID(id, "main"), workDir, model), true, nil
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
		return s, false, nil
	}
	if !IsNotFound(err) {
		return nil, false, err
	}
	return NewSession(id, workDir, model), true, nil
}

func (m *Manager) Save(ctx context.Context, s *Session) error {
	if m == nil || m.store == nil || s == nil {
		return nil
	}
	s.Messages = StripEphemeralContextMessages(s.Messages)
	s.EnsureMessageTimestamps()
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
	now := time.Now()
	s.EnsureMessageTimestamps()
	s.Messages = append(s.Messages, messages...)
	for range messages {
		s.MessageTimestamps = append(s.MessageTimestamps, now)
	}
	s.Metadata.UpdatedAt = now
}

func (s *Session) ReplaceMessages(messages []sdk.MessageParam) {
	if s == nil {
		return
	}
	now := time.Now()
	s.EnsureMessageTimestamps()
	timestamps := append([]time.Time(nil), s.MessageTimestamps...)
	if len(timestamps) > len(messages) {
		timestamps = timestamps[:len(messages)]
	}
	for len(timestamps) < len(messages) {
		timestamps = append(timestamps, now)
	}
	s.Messages = append([]sdk.MessageParam(nil), messages...)
	s.MessageTimestamps = timestamps
	s.Metadata.UpdatedAt = now
}

// EnsureMessageTimestamps keeps message timestamps aligned with Messages.
// Sessions saved before timestamps were introduced use their last update time
// as a stable fallback rather than displaying the time they are reopened.
func (s *Session) EnsureMessageTimestamps() {
	if s == nil {
		return
	}
	if len(s.MessageTimestamps) > len(s.Messages) {
		s.MessageTimestamps = s.MessageTimestamps[:len(s.Messages)]
	}
	fallback := s.Metadata.UpdatedAt
	if fallback.IsZero() {
		fallback = s.Metadata.CreatedAt
	}
	if fallback.IsZero() {
		fallback = time.Now()
	}
	for len(s.MessageTimestamps) < len(s.Messages) {
		s.MessageTimestamps = append(s.MessageTimestamps, fallback)
	}
}

// MessageTimestamp returns the persisted timestamp for a message. It provides
// a stable metadata fallback for sessions created before per-message timestamps
// were persisted.
func (s *Session) MessageTimestamp(index int) time.Time {
	if s == nil || index < 0 || index >= len(s.Messages) {
		return time.Time{}
	}
	if index < len(s.MessageTimestamps) && !s.MessageTimestamps[index].IsZero() {
		return s.MessageTimestamps[index]
	}
	if !s.Metadata.UpdatedAt.IsZero() {
		return s.Metadata.UpdatedAt
	}
	return s.Metadata.CreatedAt
}

func (s *Session) CopyMessages() []sdk.MessageParam {
	if s == nil || len(s.Messages) == 0 {
		return nil
	}
	out := make([]sdk.MessageParam, len(s.Messages))
	copy(out, s.Messages)
	return out
}

func (s *Session) HasCompactedSummaryMessage() bool {
	return s.hasCompactedContextMessage(IsCompactedSummaryMessage)
}

func (s *Session) HasCompactedProjectKnowledgeMessage() bool {
	return s.hasCompactedContextMessage(IsCompactedProjectKnowledgeMessage)
}

func (s *Session) hasCompactedContextMessage(matches func(sdk.MessageParam) bool) bool {
	if s == nil {
		return false
	}
	for _, message := range s.Messages {
		if matches(message) {
			return true
		}
	}
	return false
}

// CompactedContextMessages builds the durable leading context messages created
// by a compaction. Summary is first and project knowledge is second.
func CompactedContextMessages(summary, projectKnowledge string) []sdk.MessageParam {
	messages := make([]sdk.MessageParam, 0, 2)
	if summary = strings.TrimSpace(summary); summary != "" {
		messages = append(messages, NewCompactedSummaryMessage(summary))
	}
	if projectKnowledge = strings.TrimSpace(projectKnowledge); projectKnowledge != "" {
		messages = append(messages, NewCompactedProjectKnowledgeMessage(projectKnowledge))
	}
	return messages
}

// StripCompactedSummaryMessages removes the durable copy of a prior summary
// before generating its replacement. Session.Summary is supplied to the
// summary writer separately as the previous summary.
func StripCompactedSummaryMessages(messages []sdk.MessageParam) []sdk.MessageParam {
	out := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		if !IsCompactedSummaryMessage(message) {
			out = append(out, message)
		}
	}
	return out
}

// StripCompactedContextMessages removes durable compaction context before a
// new compaction replaces it with the current summary and project knowledge.
func StripCompactedContextMessages(messages []sdk.MessageParam) []sdk.MessageParam {
	out := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		if !IsCompactedSummaryMessage(message) && !IsCompactedProjectKnowledgeMessage(message) {
			out = append(out, message)
		}
	}
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
