package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/organizer"
	"github.com/solosw/solcode/internal/organizer/yzma"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/sessionmemory"
)

// organizerRuntime holds the optional local memory organizer (Letta archival
// write path). It is constructed lazily from config and closed with the App.
type organizerRuntime struct {
	org *organizer.Organizer

	mu       sync.Mutex
	inflight map[string]bool
}

func newOrganizerRuntime(cfg config.Config) *organizerRuntime {
	if !cfg.OrganizerEnabled() {
		return nil
	}
	gen := yzma.New(yzma.Config{
		ModelPath:   cfg.Memory.Organizer.ModelPath,
		LibDir:      cfg.OrganizerLibDir(),
		ContextSize: cfg.Memory.Organizer.ContextSize,
		Threads:     cfg.Memory.Organizer.Threads,
		GPULayers:   cfg.Memory.Organizer.GPULayers,
	})
	org := organizer.New(gen, organizer.Options{
		MaxOutputTokens: cfg.Memory.Organizer.MaxOutputTokens,
		Temperature:     cfg.Memory.Organizer.Temperature,
		TimeoutSec:      cfg.Memory.Organizer.TimeoutSec,
	})
	return &organizerRuntime{org: org, inflight: map[string]bool{}}
}

func (r *organizerRuntime) Close() error {
	if r == nil || r.org == nil {
		return nil
	}
	return r.org.Close()
}

func (r *organizerRuntime) Available() bool {
	return r != nil && r.org != nil && r.org.Available()
}

// runOrganizerAfterCompact is the MemGPT-style archival write after context is
// compacted (evicted from the working window). Failures never fail compact.
func (a *App) runOrganizerAfterCompact(ctx context.Context, current *session.Session, previousSummary, nextSummary, transcript string) {
	input := a.buildOrganizerInput(ctx, current, "compact", previousSummary, nextSummary, transcript, "", "")
	a.scheduleOrganizer(ctx, current, input)
}

// runOrganizerAfterTurn runs the local organizer after a user prompt finishes
// successfully. Callers must gate with shouldRunOrganizerAfterTurn first so
// cancel / interrupt / agent error never schedule work. Failures never fail
// the turn response path.
func (a *App) runOrganizerAfterTurn(ctx context.Context, current *session.Session, prompt, assistantOutput string) {
	prev := ""
	if current != nil {
		prev = strings.TrimSpace(current.Summary)
	}
	// Prefer the full session transcript (includes tool use/results) over the
	// thin user/assistant pair so the local model sees what actually happened.
	transcript := ""
	if current != nil {
		transcript = strings.TrimSpace(session.Transcript(session.StripEphemeralContextMessages(current.CopyMessages())))
	}
	if transcript == "" {
		transcript = buildOrganizerTurnTranscript(prompt, assistantOutput)
	}
	input := a.buildOrganizerInput(ctx, current, "turn", prev, "", transcript, prompt, assistantOutput)
	a.scheduleOrganizer(ctx, current, input)
}

// shouldRunOrganizerAfterTurn reports whether a finished prompt should be handed
// to the local organizer. Cancel, deadline, interrupt, and any agent error skip
// summarization — "等等/取消" must not write memories.
func shouldRunOrganizerAfterTurn(ctx context.Context, agentErr string) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	agentErr = strings.TrimSpace(agentErr)
	if agentErr == "" {
		return true
	}
	lower := strings.ToLower(agentErr)
	switch {
	case strings.Contains(lower, context.Canceled.Error()):
		return false
	case strings.Contains(lower, context.DeadlineExceeded.Error()):
		return false
	case strings.Contains(lower, "interrupted"), strings.Contains(lower, "interrupt"):
		return false
	case strings.Contains(lower, "aborted"), strings.Contains(lower, "abort"):
		return false
	default:
		// Any other agent failure also skips — only clean completions organize.
		return false
	}
}

func buildOrganizerTurnTranscript(prompt, assistantOutput string) string {
	var b strings.Builder
	if p := strings.TrimSpace(prompt); p != "" {
		b.WriteString("user: ")
		b.WriteString(p)
	}
	if o := strings.TrimSpace(assistantOutput); o != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("assistant: ")
		b.WriteString(o)
	}
	return strings.TrimSpace(b.String())
}

// scheduleOrganizer starts a background Organize for one trigger. Concurrent
// runs of the same session+trigger are collapsed; turn and compact may both be
// scheduled (the generator serializes inference). Failures are events only.
func (a *App) scheduleOrganizer(ctx context.Context, current *session.Session, input organizer.Input) {
	if a == nil || current == nil || a.organizer == nil || !a.Config.OrganizerEnabled() {
		return
	}
	sessionID := string(current.Metadata.ID)
	workDir := current.Metadata.WorkDir
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	input.SessionID = sessionID
	input.WorkDir = workDir
	if strings.TrimSpace(input.Transcript) == "" &&
		strings.TrimSpace(input.NextSummary) == "" &&
		len(input.ChangedFiles) == 0 &&
		len(input.ToolFacts) == 0 &&
		len(input.Todos) == 0 {
		return
	}
	if strings.TrimSpace(input.Transcript) == "" {
		// Keep Organize() happy: it requires a non-empty transcript. Prefer the
		// structured side-context folded into a short stand-in.
		input.Transcript = organizerFallbackTranscript(input)
	}
	trigger := strings.TrimSpace(input.Trigger)
	if trigger == "" {
		trigger = "organize"
		input.Trigger = trigger
	}
	key := sessionID + "|" + trigger

	a.organizer.mu.Lock()
	if a.organizer.inflight[key] {
		a.organizer.mu.Unlock()
		return
	}
	a.organizer.inflight[key] = true
	a.organizer.mu.Unlock()

	runInput := input
	run := func(runCtx context.Context) {
		defer func() {
			if a.organizer == nil {
				return
			}
			a.organizer.mu.Lock()
			delete(a.organizer.inflight, key)
			a.organizer.mu.Unlock()
		}()
		if err := a.applyOrganizerResult(runCtx, runInput); err != nil {
			a.recordCompactEvent("organizer_failed", map[string]any{
				"session_id": sessionID,
				"trigger":    trigger,
				"error":      err.Error(),
			})
			return
		}
	}

	// Local GGUF inference can take minutes; do not block the agent turn.
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), organizerBackgroundTimeout(a.Config))
		defer cancel()
		run(bg)
	}()
	_ = ctx
}

func organizerFallbackTranscript(input organizer.Input) string {
	var b strings.Builder
	if s := strings.TrimSpace(input.NextSummary); s != "" {
		b.WriteString("summary: ")
		b.WriteString(s)
	}
	if s := strings.TrimSpace(input.PreviousSummary); s != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("previous_summary: ")
		b.WriteString(s)
	}
	for _, fact := range input.ToolFacts {
		if strings.TrimSpace(fact) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(fact))
	}
	return strings.TrimSpace(b.String())
}

func organizerBackgroundTimeout(cfg config.Config) time.Duration {
	sec := cfg.Memory.Organizer.TimeoutSec
	if sec <= 0 {
		sec = 180
	}
	if sec < 60 {
		sec = 60
	}
	// Allow a little headroom beyond the per-request organizer timeout.
	return time.Duration(sec+30) * time.Second
}

func (a *App) applyOrganizerResult(ctx context.Context, input organizer.Input) error {
	if a == nil || a.organizer == nil || a.organizer.org == nil {
		return fmt.Errorf("organizer is not configured")
	}
	if !a.organizer.org.Available() {
		return fmt.Errorf("%w: local model not ready", organizer.ErrUnavailable)
	}

	sessionID := strings.TrimSpace(input.SessionID)
	workDir := strings.TrimSpace(input.WorkDir)
	result, err := a.organizer.org.Organize(ctx, input)
	if err != nil {
		return err
	}

	turn, files := a.checkpointTurnAndFiles(sessionID, workDir)
	if len(input.ChangedFiles) > 0 {
		// Prefer the pruned set already attached to the organize request when
		// checkpoint capture is empty or noisier.
		if len(files) == 0 {
			files = append([]string(nil), input.ChangedFiles...)
		}
	}
	ref := memory.FormatSessionMemoryRef(sessionID, turn)

	// Archival layer first so recall can point at the stored Item IDs.
	storedIDs := make([]string, 0, len(result.Candidates))
	stored := 0
	if a.MemoryManager != nil && a.Config.Memory.Enabled {
		for _, candidate := range result.Candidates {
			outcome, err := a.MemoryManager.RememberOrganizerCandidate(ctx, memory.OrganizerCandidateInput{
				Text:             candidate.Text,
				Kind:             memory.Kind(candidate.Kind),
				Scope:            memory.Scope(candidate.Scope),
				Tier:             memory.Tier(candidate.Tier),
				Confidence:       candidate.Confidence,
				Tags:             candidate.Tags,
				Reason:           candidate.Reason,
				SourceSessionID:  sessionID,
				SourceTurn:       turn,
				SessionMemoryRef: ref,
				Model:            result.Model,
				Status:           candidate.Status,
				Supersedes:       candidate.Supersedes,
			})
			if err != nil {
				return err
			}
			if outcome.Stored {
				stored++
				if id := strings.TrimSpace(outcome.Item.ID); id != "" {
					storedIDs = append(storedIDs, id)
				}
			}
		}
	}

	// Recall layer: chronological session log (MemGPT "recall memory").
	if err := a.writeOrganizerSessionMemory(ctx, sessionID, workDir, turn, files, storedIDs, result); err != nil {
		a.recordCompactEvent("organizer_session_memory_failed", map[string]any{
			"session_id": sessionID,
			"error":      err.Error(),
		})
	}

	// Lifecycle consolidation after archival writes (promote / decay / delete).
	if a.MemoryManager != nil {
		if err := a.MemoryManager.Consolidate(ctx); err != nil {
			a.recordCompactEvent("memory_consolidate_failed", map[string]any{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
	}

	a.recordCompactEvent("organizer_succeeded", map[string]any{
		"session_id":  sessionID,
		"model":       result.Model,
		"elapsed_ms":  result.Elapsed.Milliseconds(),
		"candidates":  len(result.Candidates),
		"stored":      stored,
		"summary_len": len([]rune(result.Summary)),
		"keywords":    len(result.Keywords),
		"turn":        turn,
	})
	return nil
}

func (a *App) writeOrganizerSessionMemory(ctx context.Context, sessionID, workDir string, turn int, files, memoryIDs []string, result organizer.Result) error {
	store := a.sessionMemoryStore(workDir)
	if store == nil {
		return fmt.Errorf("session memory store unavailable")
	}
	summary := strings.TrimSpace(result.Summary)
	if summary == "" {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(summary), "organizer:") {
		summary = "Organizer: " + summary
	}
	_, _, err := store.UpsertBySessionTurn(ctx, sessionmemory.Entry{
		Keywords:   append([]string{"organizer", "archival"}, result.Keywords...),
		Summary:    summary,
		Importance: result.Importance,
		Turn:       turn,
		Files:      files,
		MemoryIDs:  memoryIDs,
		Time:       time.Now(),
		SessionID:  sessionID,
	})
	return err
}

// buildOrganizerInput assembles the structured side-context the local model
// needs beyond a thin user/assistant pair: changed files, todos, deterministic
// tool facts, and short related archival snippets for supersede decisions.
func (a *App) buildOrganizerInput(ctx context.Context, current *session.Session, trigger, previousSummary, nextSummary, transcript, prompt, assistantOutput string) organizer.Input {
	workDir := ""
	sessionID := ""
	if current != nil {
		workDir = strings.TrimSpace(current.Metadata.WorkDir)
		sessionID = string(current.Metadata.ID)
	}
	if workDir == "" && a != nil {
		workDir = a.Config.WorkDir
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		transcript = buildOrganizerTurnTranscript(prompt, assistantOutput)
	}
	if transcript == "" {
		transcript = strings.TrimSpace(nextSummary)
	}

	task := sessionTaskContext(prompt, assistantOutput)
	if task == "" {
		task = sessionTaskContext(previousSummary, nextSummary)
	}
	_, files := a.checkpointTurnAndFiles(sessionID, workDir)
	files = a.pruneSessionFiles(ctx, task, files)

	todos := formatMergedTodos(a.judgeCurrentTodos(ctx, workDir, task))
	toolFacts := memory.ToolTraceFacts(transcript)
	related := a.relatedOrganizerMemories(ctx, sessionID, transcript, prompt, assistantOutput, nextSummary)

	return organizer.Input{
		SessionID:       sessionID,
		WorkDir:         workDir,
		Transcript:      transcript,
		PreviousSummary: strings.TrimSpace(previousSummary),
		NextSummary:     strings.TrimSpace(nextSummary),
		Trigger:         strings.TrimSpace(trigger),
		ChangedFiles:    files,
		Todos:           todos,
		ToolFacts:       toolFacts,
		RelatedMemories: related,
	}
}

func (a *App) relatedOrganizerMemories(ctx context.Context, sessionID, transcript, prompt, assistantOutput, nextSummary string) []string {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return nil
	}
	query := strings.TrimSpace(assistantOutput)
	if query == "" {
		query = strings.TrimSpace(prompt)
	}
	if query == "" {
		query = strings.TrimSpace(nextSummary)
	}
	if query == "" {
		// Fall back to a short head of the transcript so Retrieve still has a cue.
		query = transcript
	}
	if runes := []rune(query); len(runes) > 400 {
		query = string(runes[:400])
	}
	if strings.TrimSpace(query) == "" {
		return nil
	}
	items, err := a.MemoryManager.Retrieve(ctx, query, sessionID, true, 6)
	if err != nil || len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		id := strings.TrimSpace(item.ID)
		line := text
		if id != "" {
			line = id + ": " + text
		}
		if kind := strings.TrimSpace(string(item.Kind)); kind != "" {
			line = "[" + kind + "] " + line
		}
		out = append(out, line)
		if len(out) >= 6 {
			break
		}
	}
	return out
}
