// Package sessionmemory persists model-authored session memories into the
// project's solcode.md so later sessions can recall what happened.
package sessionmemory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/config"
)

// FileName is the markdown file session memories are appended to.
const FileName = "solcode.md"

const (
	// Header is prepended once when the file is first created.
	Header = "# Session memory\n\nWritten by the model at session end. Newest entries last.\n"
	// maxSummaryRunes bounds one entry's summary.
	maxSummaryRunes = 2000
	// maxKeywords caps keywords per entry.
	maxKeywords = 12
	// maxFiles caps changed-file paths recorded per entry.
	maxFiles = 40
)

// Path returns the session memory file for a working directory:
// <workDir>/.solcode/solcode.md.
func Path(workDir string) string {
	dir := config.ProjectConfigDir(workDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, FileName)
}

// Entry is one session memory record.
type Entry struct {
	Keywords   []string
	Summary    string
	Importance float64
	Turn       int
	Files      []string
	// Todos is the todolist snapshot for this turn, optionally annotated by Jev.
	Todos     []TodoJudgment
	Time      time.Time
	SessionID string
}

type entryHeader struct {
	time       time.Time
	sessionID  string
	turn       int
	importance float64
}

// Store appends and reads session memories in a solcode.md file.
type Store struct {
	path string
}

// NewStore opens the session memory file for a working directory.
func NewStore(workDir string) *Store {
	return &Store{path: Path(workDir)}
}

// NewStoreAt opens a specific file path (used by tests).
func NewStoreAt(path string) *Store {
	return &Store{path: filepath.Clean(strings.TrimSpace(path))}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Append writes one entry, creating the file and header when needed.
// Returns the normalized entry.
func (s *Store) Append(ctx context.Context, entry Entry) (Entry, error) {
	if s == nil || s.path == "" {
		return Entry{}, fmt.Errorf("session memory path is empty")
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}

	entry, err := normalizeEntry(entry)
	if err != nil {
		return Entry{}, err
	}
	if err := s.ensureFile(); err != nil {
		return Entry{}, err
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Entry{}, fmt.Errorf("open session memory: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(formatEntry(entry)); err != nil {
		return Entry{}, fmt.Errorf("append session memory: %w", err)
	}
	return entry, nil
}

// UpsertBySessionTurn keeps one entry per (SessionID, Turn). When an entry for
// that pair already exists, keywords/files are unioned, todos are replaced by
// the incoming (latest) snapshot, and summary/time/importance take the new
// values. Otherwise the entry is appended.
func (s *Store) UpsertBySessionTurn(ctx context.Context, entry Entry) (Entry, bool, error) {
	if s == nil || s.path == "" {
		return Entry{}, false, fmt.Errorf("session memory path is empty")
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}

	entry, err := normalizeEntry(entry)
	if err != nil {
		return Entry{}, false, err
	}

	existing, err := s.List(ctx)
	if err != nil {
		return Entry{}, false, err
	}

	idx := -1
	for i := len(existing) - 1; i >= 0; i-- {
		if existing[i].SessionID == entry.SessionID && existing[i].Turn == entry.Turn {
			idx = i
			break
		}
	}
	if idx < 0 {
		stored, err := s.Append(ctx, entry)
		return stored, false, err
	}

	merged := mergeSessionTurnEntry(existing[idx], entry)
	existing[idx] = merged
	if err := s.rewrite(existing); err != nil {
		return Entry{}, false, err
	}
	return merged, true, nil
}

func normalizeEntry(entry Entry) (Entry, error) {
	entry.Summary = strings.TrimSpace(entry.Summary)
	if entry.Summary == "" {
		return Entry{}, fmt.Errorf("summary is required")
	}
	if len([]rune(entry.Summary)) > maxSummaryRunes {
		return Entry{}, fmt.Errorf("summary is too long (%d runes, max %d)", len([]rune(entry.Summary)), maxSummaryRunes)
	}
	entry.Keywords = normalizeKeywords(entry.Keywords)
	if entry.Importance <= 0 {
		entry.Importance = 0.5
	}
	if entry.Importance > 1 {
		entry.Importance = 1
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	entry.Files = normalizeFiles(entry.Files)
	entry.SessionID = strings.TrimSpace(entry.SessionID)
	return entry, nil
}

func mergeSessionTurnEntry(prev, next Entry) Entry {
	out := next
	out.Keywords = normalizeKeywords(append(append([]string{}, prev.Keywords...), next.Keywords...))
	out.Files = normalizeFiles(append(append([]string{}, prev.Files...), next.Files...))
	// Todos come from the newest write: TodoWrite is a full-list replacement,
	// and turn-end / model-authored memories already carry the latest snapshot.
	out.Todos = next.Todos
	out.Summary = next.Summary
	out.Importance = next.Importance
	out.Time = next.Time
	out.SessionID = next.SessionID
	out.Turn = next.Turn
	return out
}

func (s *Store) ensureFile() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session memory dir: %w", err)
	}
	existing, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read session memory: %w", err)
	}
	if len(strings.TrimSpace(string(existing))) == 0 {
		if err := os.WriteFile(s.path, []byte(Header), 0o644); err != nil {
			return fmt.Errorf("write session memory header: %w", err)
		}
	}
	return nil
}

func (s *Store) rewrite(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session memory dir: %w", err)
	}
	var b strings.Builder
	b.WriteString(Header)
	for _, entry := range entries {
		b.WriteString(formatEntry(entry))
	}
	if err := os.WriteFile(s.path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("rewrite session memory: %w", err)
	}
	return nil
}

// Read returns entries newest-first. When sessionID is non-empty, only entries
// for that session are considered. When query is non-empty, entries are filtered
// by fuzzy keyword/summary match and ranked by relevance.
func (s *Store) Read(ctx context.Context, query string, limit int) ([]Entry, error) {
	return s.ReadForSession(ctx, "", query, limit)
}

// ReadForSession is like Read but restricts results to sessionID when set.
func (s *Store) ReadForSession(ctx context.Context, sessionID, query string, limit int) ([]Entry, error) {
	if s == nil || s.path == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	entries, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	entries = filterEntriesBySession(entries, sessionID)
	// List is oldest-first; recent reads want newest-first.
	reverseEntries(entries)

	query = strings.TrimSpace(query)
	if query == "" {
		if len(entries) > limit {
			entries = entries[:limit]
		}
		return entries, nil
	}

	terms := queryTerms(query)
	lower := strings.ToLower(query)
	type scored struct {
		entry   Entry
		content float64
	}
	var hits []scored
	for _, entry := range entries {
		content := 0.0
		haystack := strings.ToLower(entry.Summary)
		keywords := make([]string, 0, len(entry.Keywords))
		for _, kw := range entry.Keywords {
			keywords = append(keywords, strings.ToLower(kw))
		}
		for _, term := range terms {
			if keywordContains(keywords, term) {
				content += 5
			}
			if strings.Contains(haystack, term) {
				content += 2
			}
		}
		// Whole-query substring is a strong signal (e.g. "checkpoint rewind").
		if lower != "" && strings.Contains(haystack, lower) {
			content += 4
		}
		if content == 0 {
			continue
		}
		hits = append(hits, scored{entry: entry, content: content})
	}

	contentVals := make([]float64, len(hits))
	turnVals := make([]float64, len(hits))
	minTurn, maxTurn := 0, 0
	sawTurn := false
	for i, hit := range hits {
		contentVals[i] = hit.content
		turnVals[i] = float64(hit.entry.Turn)
		if !sawTurn {
			minTurn, maxTurn = hit.entry.Turn, hit.entry.Turn
			sawTurn = true
			continue
		}
		if hit.entry.Turn < minTurn {
			minTurn = hit.entry.Turn
		}
		if hit.entry.Turn > maxTurn {
			maxTurn = hit.entry.Turn
		}
	}
	contentNorm := minMaxNormalizeFloats(contentVals)
	turnNorm := make([]float64, len(hits))
	if sawTurn && minTurn == maxTurn {
		for i := range turnNorm {
			turnNorm[i] = 0.5
		}
	} else if sawTurn {
		span := float64(maxTurn - minTurn)
		for i, hit := range hits {
			turnNorm[i] = float64(hit.entry.Turn-minTurn) / span
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		si := 0.5*contentNorm[i] + 0.5*turnNorm[i]
		sj := 0.5*contentNorm[j] + 0.5*turnNorm[j]
		if si == sj {
			if hits[i].entry.Turn != hits[j].entry.Turn {
				return hits[i].entry.Turn > hits[j].entry.Turn
			}
			return hits[i].entry.Time.After(hits[j].entry.Time)
		}
		return si > sj
	})
	out := make([]Entry, 0, min(limit, len(hits)))
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].entry)
	}
	return out, nil
}

func minMaxNormalizeFloats(values []float64) []float64 {
	out := make([]float64, len(values))
	if len(values) == 0 {
		return out
	}
	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if minV == maxV {
		for i := range out {
			out[i] = 0.5
		}
		return out
	}
	span := maxV - minV
	for i, v := range values {
		out[i] = (v - minV) / span
	}
	return out
}

func filterEntriesBySession(entries []Entry, sessionID string) []Entry {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.SessionID) == sessionID {
			out = append(out, entry)
		}
	}
	return out
}

// List returns all entries in file order (oldest-first).
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	if s == nil || s.path == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session memory: %w", err)
	}
	return parseEntries(string(data)), nil
}

func formatEntry(entry Entry) string {
	var b strings.Builder
	b.WriteString("\n## ")
	b.WriteString(entry.Time.Format("2006-01-02 15:04:05"))
	if entry.SessionID != "" {
		b.WriteString(" · session ")
		b.WriteString(entry.SessionID)
	}
	b.WriteString(fmt.Sprintf(" · turn %d · importance %.2f\n", entry.Turn, entry.Importance))
	if len(entry.Keywords) > 0 {
		b.WriteString("- keywords: ")
		b.WriteString(strings.Join(entry.Keywords, ", "))
		b.WriteString("\n")
	}
	if len(entry.Files) > 0 {
		b.WriteString("- files: ")
		b.WriteString(strings.Join(entry.Files, ", "))
		b.WriteString("\n")
	}
	if formatted := formatTodos(entry.Todos); formatted != "" {
		b.WriteString("- todos: ")
		b.WriteString(formatted)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(entry.Summary)
	b.WriteString("\n")
	return b.String()
}

var headingRe = regexp.MustCompile(`^##\s+(.*)$`)

// parseEntries reads back the format written by formatEntry. Unknown content is
// ignored so a hand-edited file does not break reads.
func parseEntries(text string) []Entry {
	lines := strings.Split(text, "\n")
	var entries []Entry
	var current *Entry
	var summary strings.Builder

	flush := func() {
		if current == nil {
			return
		}
		current.Summary = strings.TrimSpace(summary.String())
		if current.Summary != "" {
			entries = append(entries, *current)
		}
		current = nil
		summary.Reset()
	}

	for _, line := range lines {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			hdr, ok := parseHeading(m[1])
			if !ok {
				continue
			}
			current = &Entry{
				Time:       hdr.time,
				SessionID:  hdr.sessionID,
				Turn:       hdr.turn,
				Importance: hdr.importance,
			}
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- keywords:"):
			current.Keywords = splitList(strings.TrimPrefix(trimmed, "- keywords:"))
		case strings.HasPrefix(trimmed, "- files:"):
			current.Files = splitList(strings.TrimPrefix(trimmed, "- files:"))
		case strings.HasPrefix(trimmed, "- todos:"):
			current.Todos = parseTodos(strings.TrimPrefix(trimmed, "- todos:"))
		default:
			summary.WriteString(line)
			summary.WriteString("\n")
		}
	}
	flush()
	return entries
}

func parseHeading(text string) (entryHeader, bool) {
	var hdr entryHeader
	parts := strings.Split(text, "·")
	if len(parts) == 0 {
		return hdr, false
	}
	when := strings.TrimSpace(parts[0])
	t, err := time.ParseInLocation("2006-01-02 15:04:05", when, time.Local)
	if err != nil {
		return hdr, false
	}
	hdr.time = t
	for _, part := range parts[1:] {
		field := strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(field, "session "):
			hdr.sessionID = strings.TrimSpace(strings.TrimPrefix(field, "session "))
		case strings.HasPrefix(field, "turn "):
			fmt.Sscanf(strings.TrimPrefix(field, "turn "), "%d", &hdr.turn)
		case strings.HasPrefix(field, "importance "):
			fmt.Sscanf(strings.TrimPrefix(field, "importance "), "%f", &hdr.importance)
		}
	}
	return hdr, true
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeKeywords(keywords []string) []string {
	out := make([]string, 0, len(keywords))
	seen := map[string]bool{}
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		key := strings.ToLower(kw)
		if kw == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, kw)
		if len(out) == maxKeywords {
			break
		}
	}
	return out
}

func normalizeFiles(files []string) []string {
	out := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimSpace(filepath.ToSlash(f))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == maxFiles {
			break
		}
	}
	sort.Strings(out)
	return out
}

func keywordContains(keywords []string, term string) bool {
	for _, kw := range keywords {
		if strings.Contains(kw, term) || strings.Contains(term, kw) {
			return true
		}
	}
	return false
}

func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r < 0x80
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if len([]rune(f)) < 2 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		// Non-ASCII queries (e.g. Chinese) keep the whole trimmed query.
		if q := strings.TrimSpace(strings.ToLower(query)); q != "" {
			out = append(out, q)
		}
	}
	return out
}

func reverseEntries(entries []Entry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}