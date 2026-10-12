package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/solosw/solcode/internal/tool"
)

// WorkingSet is the per-run task working set described by the Efficient Engine
// plan (§5). It tracks what the agent knows about the current task so context
// can be reused instead of re-explored:
//
//   - the current task goal
//   - files read or modified, with a content hash and (bounded) cached content
//   - confirmed facts and open questions
//
// It is updated locally from tool results only. It never issues a model request
// and never writes a summary through the LLM.
type WorkingSet struct {
	mu sync.Mutex

	goal  string
	turn  int
	files map[string]*workingFile
	facts []string
	open  []string

	// Bounds keep the set from growing without limit on long runs.
	maxFiles     int
	maxCacheSize int
}

// workingFile records what is known about one file. content is cached only when
// it fits the per-file budget; a nil/empty cache still keeps the version hash so
// stale references can be detected.
type workingFile struct {
	path     string
	hash     string
	content  string
	cached   bool
	modified bool
	verified bool
	turn     int
}

const (
	defaultWorkingSetMaxFiles = 64
	defaultWorkingSetCacheMax = 16 * 1024
)

// NewWorkingSet builds an empty working set with default bounds.
func NewWorkingSet() *WorkingSet {
	return &WorkingSet{
		files:        map[string]*workingFile{},
		maxFiles:     defaultWorkingSetMaxFiles,
		maxCacheSize: defaultWorkingSetCacheMax,
	}
}

// SetGoal records the current task goal. The newest goal wins.
func (w *WorkingSet) SetGoal(goal string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if trimmed := strings.TrimSpace(goal); trimmed != "" {
		w.goal = trimmed
	}
}

// BeginTurn advances the internal turn counter so eviction favors recent work.
func (w *WorkingSet) BeginTurn(turn int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.turn = turn
}

// ObserveFile records a file the agent just touched. content is hashed and
// cached (bounded). mutated marks a file the agent changed, which invalidates
// any previously cached content and clears its verification flag.
func (w *WorkingSet) ObserveFile(path string, content []byte, existed, mutated bool) {
	if w == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := w.files[path]
	if entry == nil {
		entry = &workingFile{path: path}
		w.files[path] = entry
	}
	entry.turn = w.turn
	if !existed {
		// A deleted target: keep the path so dependents can invalidate, but drop
		// content and mark it modified.
		entry.hash = ""
		entry.content = ""
		entry.cached = false
		entry.modified = true
		entry.verified = false
		w.evictLocked()
		return
	}
	sum := sha256.Sum256(content)
	entry.hash = hex.EncodeToString(sum[:])[:12]
	if len(content) <= w.maxCacheSize {
		entry.content = string(content)
		entry.cached = true
	} else {
		entry.content = ""
		entry.cached = false
	}
	if mutated {
		entry.modified = true
		entry.verified = false
	}
	w.evictLocked()
}

// Invalidate drops a file's cached content because it changed elsewhere. The
// path and its stale hash are retained so the model can be told to re-read.
func (w *WorkingSet) Invalidate(path string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry := w.files[strings.TrimSpace(path)]; entry != nil {
		entry.content = ""
		entry.cached = false
		entry.modified = true
		entry.verified = false
	}
}

// MarkVerified notes that a modified file has been verified.
func (w *WorkingSet) MarkVerified(path string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry := w.files[strings.TrimSpace(path)]; entry != nil {
		entry.verified = true
	}
}

// AddFact records a confirmed fact for the current task.
func (w *WorkingSet) AddFact(fact string) {
	if w == nil {
		return
	}
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, existing := range w.facts {
		if existing == fact {
			return
		}
	}
	w.facts = append(w.facts, fact)
	if len(w.facts) > 32 {
		w.facts = w.facts[len(w.facts)-32:]
	}
}

// AddOpen records an unresolved issue / next step.
func (w *WorkingSet) AddOpen(issue string) {
	if w == nil {
		return
	}
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, existing := range w.open {
		if existing == issue {
			return
		}
	}
	w.open = append(w.open, issue)
	if len(w.open) > 32 {
		w.open = w.open[len(w.open)-32:]
	}
}

// Lookup returns the cached content for path when it is still valid.
func (w *WorkingSet) Lookup(path string) (string, bool) {
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	entry := w.files[strings.TrimSpace(path)]
	if entry == nil || !entry.cached {
		return "", false
	}
	return entry.content, true
}

// Hash returns the recorded version hash for path.
func (w *WorkingSet) Hash(path string) (string, bool) {
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	entry := w.files[strings.TrimSpace(path)]
	if entry == nil || entry.hash == "" {
		return "", false
	}
	return entry.hash, true
}

func (w *WorkingSet) evictLocked() {
	if len(w.files) <= w.maxFiles {
		return
	}
	type candidate struct {
		path string
		turn int
	}
	entries := make([]candidate, 0, len(w.files))
	for path, entry := range w.files {
		entries = append(entries, candidate{path: path, turn: entry.turn})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].turn != entries[j].turn {
			return entries[i].turn < entries[j].turn
		}
		return entries[i].path < entries[j].path
	})
	for i := 0; i < len(entries) && len(w.files) > w.maxFiles; i++ {
		delete(w.files, entries[i].path)
	}
}

// InjectionText renders the path/hash/fact view injected every turn. It never
// includes file contents: code already retained in the session must not be
// re-injected, and stale content is intentionally excluded.
func (w *WorkingSet) InjectionText() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.goal == "" && len(w.files) == 0 && len(w.facts) == 0 && len(w.open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Task working set (local, from tool results — not re-read from the model):")
	if w.goal != "" {
		b.WriteString("\nGoal: " + oneLine(w.goal, 200))
	}
	if len(w.files) > 0 {
		paths := make([]string, 0, len(w.files))
		for path := range w.files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		b.WriteString("\nFiles:")
		for _, path := range paths {
			entry := w.files[path]
			flags := ""
			if entry.modified {
				if entry.verified {
					flags = " [modified, verified]"
				} else {
					flags = " [modified, unverified]"
				}
			}
			hash := entry.hash
			if hash == "" {
				hash = "absent"
			}
			b.WriteString(fmt.Sprintf("\n- %s (hash %s)%s", path, hash, flags))
		}
	}
	if len(w.facts) > 0 {
		b.WriteString("\nConfirmed facts:")
		for _, fact := range w.facts {
			b.WriteString("\n- " + oneLine(fact, 200))
		}
	}
	if len(w.open) > 0 {
		b.WriteString("\nOpen:")
		for _, issue := range w.open {
			b.WriteString("\n- " + oneLine(issue, 200))
		}
	}
	return b.String()
}

// RecoveryText restores cached snippets for files that are still needed after
// the transcript was compacted. It is meant to be injected only when compaction
// dropped the code the agent still has to edit, and only for the provided
// paths (the caller decides which are still needed).
func (w *WorkingSet) RecoveryText(paths []string) string {
	if w == nil || len(paths) == 0 {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var b strings.Builder
	for _, path := range paths {
		entry := w.files[strings.TrimSpace(path)]
		if entry == nil || !entry.cached || entry.content == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("Restored from local cache (transcript was compacted; content unchanged since capture):")
		}
		b.WriteString(fmt.Sprintf("\n\n%s (hash %s):\n%s", entry.path, entry.hash, entry.content))
	}
	return b.String()
}

// ModifiedPaths lists files the agent changed this run, newest turn first.
func (w *WorkingSet) ModifiedPaths() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	type candidate struct {
		path string
		turn int
	}
	var entries []candidate
	for path, entry := range w.files {
		if entry.modified {
			entries = append(entries, candidate{path: path, turn: entry.turn})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].turn != entries[j].turn {
			return entries[i].turn > entries[j].turn
		}
		return entries[i].path < entries[j].path
	})
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.path)
	}
	return out
}

// ReadFileBounded reads a file for working-set observation without unbounded
// memory use. It reports whether the file existed.
func ReadFileBounded(path string, limit int) ([]byte, bool) {
	if limit <= 0 {
		limit = defaultWorkingSetCacheMax
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, limit+1)
	n, _ := f.Read(buf)
	if n > limit {
		// Too large to cache; report existence with a truncated buffer so the
		// caller records a hash over a stable prefix.
		return buf[:limit], true
	}
	return buf[:n], true
}

func oneLine(text string, limit int) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) > limit {
		return string([]rune(text)[:limit]) + "…"
	}
	return text
}

// observeWorkingSet records the local effect of one finished tool call. It
// reads files from disk (never the transcript) so the recorded hash reflects
// the post-call state, and it processes BatchTool children individually.
func observeWorkingSet(ws *WorkingSet, toolName string, input []byte, uctx *tool.UseContext, result ToolResult) {
	if ws == nil || uctx == nil {
		return
	}
	if toolName == tool.BatchToolName {
		observeBatchWorkingSet(ws, input, uctx, result)
		return
	}
	switch toolName {
	case tool.ViewToolName, "LSP":
		if result.IsError {
			return
		}
		for _, path := range tool.PathsForCheckpoint(tool.EditToolName, input) {
			observeWorkingFilePath(ws, uctx, path, false)
		}
		// View uses "path"; PathsForCheckpoint("Edit") reads the same key.
	case tool.EditToolName, tool.WriteToolName, tool.MultiEditToolName, tool.MultiWriteToolName, tool.PatchToolName:
		if result.IsError {
			return
		}
		for _, path := range tool.PathsForCheckpoint(toolName, input) {
			observeWorkingFilePath(ws, uctx, path, true)
		}
	}
}

// observeBatchWorkingSet walks a BatchTool request and records each child's
// file effect. Errors are still recorded so a failed edit invalidates a stale
// cached read.
func observeBatchWorkingSet(ws *WorkingSet, input []byte, uctx *tool.UseContext, result ToolResult) {
	var req struct {
		Operations []struct {
			ID    string          `json:"id"`
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"operations"`
	}
	if json.Unmarshal(input, &req) != nil {
		return
	}
	// Parse the response to learn which child failed.
	type childStatus struct {
		Status string `json:"status"`
	}
	statuses := map[string]string{}
	if !result.IsError && result.Content != nil {
		var resp struct {
			Results []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"results"`
		}
		if json.Unmarshal([]byte(result.Content.Text), &resp) == nil {
			for _, r := range resp.Results {
				statuses[r.ID] = r.Status
			}
		}
	}
	for _, op := range req.Operations {
		failed := statuses[op.ID] != "" && statuses[op.ID] != "success"
		switch op.Tool {
		case tool.ViewToolName, "LSP":
			if failed {
				continue
			}
			for _, path := range tool.PathsForCheckpoint(tool.EditToolName, op.Input) {
				observeWorkingFilePath(ws, uctx, path, false)
			}
		case tool.EditToolName, tool.WriteToolName, tool.MultiEditToolName, tool.MultiWriteToolName, tool.PatchToolName:
			for _, path := range tool.PathsForCheckpoint(op.Tool, op.Input) {
				observeWorkingFilePath(ws, uctx, path, true)
			}
		}
	}
}

// observeWorkingFilePath reads one resolved path and records it in the working
// set. mutated marks an edit; a failed/absent read records the path so stale
// cache entries can be invalidated.
func observeWorkingFilePath(ws *WorkingSet, uctx *tool.UseContext, path string, mutated bool) {
	abs := tool.ResolvePath(uctx, path)
	if strings.TrimSpace(abs) == "" {
		return
	}
	content, existed := ReadFileBounded(abs, defaultWorkingSetCacheMax)
	ws.ObserveFile(abs, content, existed, mutated)
}
