package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

var (
	ErrNotFound  = errors.New("agent not found")
	ErrBusy      = errors.New("agent is busy")
	ErrUncertain = errors.New("agent has tool calls with unconfirmed outcomes; inspect history and resolve them before resuming")
)

// Store is implemented outside this package to avoid coupling the coordinator
// to session, permission, or tool execution. Acquire must exclude other processes
// and release automatically when its process exits.
type Store interface {
	Load(context.Context, AgentID) (*Record, error)
	Save(context.Context, *Record) error
	List(context.Context) ([]*Record, error)
	Acquire(context.Context, AgentID) (func() error, error)
	SaveFork(context.Context, Snapshot) (string, error)
	LoadFork(context.Context, string) (Snapshot, error)
}

// Scope authorizes access by project WorkDir. Parent fields describe the caller
// for compatibility and provenance; they do not restrict project-level reuse.
type Scope struct {
	ParentID        AgentID
	ParentSessionID string
	WorkDir         string
}

type TokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (u *TokenUsage) Add(v TokenUsage) {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CacheCreationInputTokens += v.CacheCreationInputTokens
	u.CacheReadInputTokens += v.CacheReadInputTokens
}

type Snapshot struct {
	Messages   []sdk.MessageParam
	WorkingSet json.RawMessage
}

// Checkpoint describes the entire active context, not the archival transcript.
// Usage is cumulative within this run. A failed checkpoint must stop execution.
type Checkpoint struct {
	Snapshot
	Usage      TokenUsage
	UsageKnown bool
	Model      string
	Stage      string
	Compacted  bool
}

type SessionRequest struct {
	Config   AgentConfig
	Snapshot Snapshot
	Save     func(Checkpoint) error
}

type PersistentRunner interface {
	RunSession(context.Context, SessionRequest) AgentResult
}

type RunRecord struct {
	ParentID        AgentID     `json:"parent_id,omitempty"`
	ParentSessionID string      `json:"parent_session_id,omitempty"`
	ID              string      `json:"run_id"`
	ExecutionID     string      `json:"execution_id"`
	Attempt         int         `json:"attempt"`
	TaskID          string      `json:"task_id,omitempty"`
	Description     string      `json:"description,omitempty"`
	Model           string      `json:"model,omitempty"`
	AllowedTools    []string    `json:"allowed_tools,omitempty"`
	State           AgentState  `json:"state"`
	StartedAt       time.Time   `json:"started_at"`
	EndedAt         *time.Time  `json:"ended_at,omitempty"`
	Result          AgentResult `json:"result"`
	Usage           TokenUsage  `json:"usage"`
}

type SessionEvent struct {
	RunID        string    `json:"run_id"`
	Kind         string    `json:"kind"`
	At           time.Time `json:"at"`
	MessageCount int       `json:"message_count"`
}

// Context contains indexes into Messages; negative indexes (-1-index) address
// the immutable ForkID snapshot. Compaction changes indexes, never deletes audit
// history. Inherited history is stored once and never copied into child files.
type Record struct {
	// ForkParentSessionID stays fixed when another project session resumes us.
	// Empty legacy records fall back to Config.ParentSessionID until resumed.
	ForkParentSessionID string      `json:"fork_parent_session_id,omitempty"`
	MessageTimestamps   []time.Time `json:"message_timestamps,omitempty"`
	Config              AgentConfig
	Status              AgentStatus
	Result              AgentResult
	Runs                []RunRecord
	Events              []SessionEvent
	Messages            []sdk.MessageParam
	Context             []int
	ForkID              string
	WorkingSet          json.RawMessage
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Archived            bool
}

func NewID(prefix string) string { return prefix + "-" + strings.ToLower(rand.Text()) }

func ValidID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (r *Record) Active(fork Snapshot) (Snapshot, error) {
	out := Snapshot{WorkingSet: append(json.RawMessage(nil), r.WorkingSet...)}
	for _, ref := range r.Context {
		source, index := r.Messages, ref
		if ref < 0 {
			source, index = fork.Messages, -1-ref
		}
		if index < 0 || index >= len(source) {
			return Snapshot{}, fmt.Errorf("invalid context reference %d", ref)
		}
		out.Messages = append(out.Messages, source[index])
	}
	return out, nil
}

func messageKey(m sdk.MessageParam) string { b, _ := json.Marshal(m); return string(b) }

func extendsToolResults(before, after sdk.MessageParam) bool {
	if string(before.Role) != "user" || string(after.Role) != "user" || len(before.Content) == 0 || len(after.Content) <= len(before.Content) {
		return false
	}
	for i, b := range before.Content {
		if b.OfToolResult == nil {
			return false
		}
		x, _ := json.Marshal(b)
		y, _ := json.Marshal(after.Content[i])
		if !bytes.Equal(x, y) {
			return false
		}
	}
	for _, b := range after.Content {
		if b.OfToolResult == nil {
			return false
		}
	}
	return true
}

func (r *Record) apply(cp Checkpoint, fork Snapshot) error {
	previous, err := r.Active(fork)
	if err != nil {
		return err
	}
	refs := make([]int, 0, len(cp.Messages))
	// Prefix comparison also handles an amended partial tool-result message.
	prefix := 0
	if !cp.Compacted {
		for prefix < len(previous.Messages) && prefix < len(cp.Messages) && messageKey(previous.Messages[prefix]) == messageKey(cp.Messages[prefix]) {
			prefix++
		}
		refs = append(refs, r.Context[:prefix]...)
		if prefix == len(previous.Messages)-1 && prefix < len(cp.Messages) && r.Context[prefix] >= 0 && extendsToolResults(previous.Messages[prefix], cp.Messages[prefix]) {
			ref := r.Context[prefix]
			r.Messages[ref] = cp.Messages[prefix]
			refs = append(refs, ref)
			prefix++
		}
	}
	known := map[string]int{}
	if cp.Compacted {
		for i, m := range fork.Messages {
			known[messageKey(m)] = -1 - i
		}
		for i, m := range r.Messages {
			known[messageKey(m)] = i
		}
	}
	for _, m := range cp.Messages[prefix:] {
		if ref, ok := known[messageKey(m)]; cp.Compacted && ok {
			refs = append(refs, ref)
			continue
		}
		refs = append(refs, len(r.Messages))
		r.Messages = append(r.Messages, m)
		r.MessageTimestamps = append(r.MessageTimestamps, time.Now())
	}
	r.Context = refs
	if len(cp.WorkingSet) != 0 && !bytes.Equal(cp.WorkingSet, r.WorkingSet) {
		r.WorkingSet = append(json.RawMessage(nil), cp.WorkingSet...)
	}
	r.UpdatedAt = time.Now()
	if len(r.Runs) > 0 {
		if cp.UsageKnown {
			r.Runs[len(r.Runs)-1].Usage = cp.Usage
		}
		if cp.Model != "" {
			r.Runs[len(r.Runs)-1].Model = cp.Model
			r.Config.Model = cp.Model
		}
	}
	r.Events = append(r.Events, SessionEvent{RunID: r.Status.RunID, Kind: cp.Stage, At: r.UpdatedAt, MessageCount: len(r.Messages)})
	return nil
}

// PendingToolIDs never replays a call. It is intentionally conservative even
// for read-only calls, since a crash may occur after effects but before Save.
func PendingToolIDs(messages []sdk.MessageParam) []string {
	pending := map[string]bool{}
	var order []string
	for _, m := range messages {
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				pending[b.OfToolUse.ID] = true
				order = append(order, b.OfToolUse.ID)
			}
			if b.OfToolResult != nil {
				delete(pending, b.OfToolResult.ToolUseID)
			}
		}
	}
	var out []string
	for _, id := range order {
		if pending[id] {
			out = append(out, id)
		}
	}
	return out
}
