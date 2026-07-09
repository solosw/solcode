package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Tier string

type Kind string

type Scope string

const (
	TierSensory    Tier = "M1"
	TierWorking    Tier = "M2"
	TierShortTerm  Tier = "M3"
	TierLongTerm   Tier = "M4"
	TierProcedural Tier = "M5"
)

const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindConstraint Kind = "constraint"
	KindTask       Kind = "task"
	KindWorkflow   Kind = "workflow"
)

const (
	ScopeSession Scope = "session"
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

type Item struct {
	ID                 string    `json:"id"`
	Tier               Tier      `json:"tier"`
	Kind               Kind      `json:"kind,omitempty"`
	Scope              Scope     `json:"scope,omitempty"`
	Text               string    `json:"text"`
	Tags               []string  `json:"tags,omitempty"`
	Importance         float64   `json:"importance"`
	Confidence         float64   `json:"confidence,omitempty"`
	RetentionScore     float64   `json:"retention_score,omitempty"`
	AccessCount        int       `json:"access_count"`
	PromotionCount     int       `json:"promotion_count,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastAccessedAt     time.Time `json:"last_accessed_at"`
	LastReinforcedAt   time.Time `json:"last_reinforced_at,omitempty"`
	SourceSessionID    string    `json:"source_session_id,omitempty"`
	DerivedFromSummary bool      `json:"derived_from_summary,omitempty"`
	JudgeReason        string    `json:"judge_reason,omitempty"`
	JudgeModel         string    `json:"judge_model,omitempty"`
	JudgeVersion       string    `json:"judge_version,omitempty"`
}

type Retriever interface {
	Retrieve(query string, limit int) []Item
}

type Summarizer interface {
	Summarize(previous string, newContent string) (string, error)
}

type Consolidator interface {
	Consolidate(items []Item) []Item
}

type FileStore struct {
	dir string
}

func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: dir}
}

func NewItem(text string, tier Tier, sourceSessionID string) Item {
	now := time.Now()
	text = strings.TrimSpace(text)
	if tier == "" {
		tier = TierLongTerm
	}
	return Item{
		ID:               stableID(text),
		Tier:             tier,
		Kind:             KindFact,
		Scope:            ScopeProject,
		Text:             text,
		Importance:       0.7,
		Confidence:       0.7,
		RetentionScore:   0.7,
		AccessCount:      1,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastAccessedAt:   now,
		LastReinforcedAt: now,
		SourceSessionID:  sourceSessionID,
	}
}

func (s *FileStore) List(ctx context.Context) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list memories: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			continue
		}
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		if item.ID == "" {
			item.ID = strings.TrimSuffix(entry.Name(), ".json")
		}
		cleaned, changed, keep := sanitizeStoredMemoryItem(item)
		if !keep {
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
			continue
		}
		if changed {
			if _, err := s.Save(ctx, cleaned); err == nil {
				item = cleaned
			} else {
				item = cleaned
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *FileStore) Save(ctx context.Context, item Item) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if s == nil || s.dir == "" {
		return item, nil
	}
	item.Text = strings.TrimSpace(item.Text)
	if item.Text == "" {
		return Item{}, fmt.Errorf("memory text is required")
	}
	now := time.Now()
	if item.ID == "" {
		item.ID = stableID(item.Text)
	}
	if item.Tier == "" {
		item.Tier = TierLongTerm
	}
	if item.Importance <= 0 {
		item.Importance = 0.7
	}
	if item.Confidence <= 0 {
		item.Confidence = 0.7
	}
	if item.RetentionScore <= 0 {
		item.RetentionScore = item.Importance
	}
	if item.Kind == "" {
		item.Kind = KindFact
	}
	if item.Scope == "" {
		item.Scope = ScopeProject
	}
	if item.AccessCount <= 0 {
		item.AccessCount = 1
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.LastAccessedAt.IsZero() {
		item.LastAccessedAt = now
	}
	if item.LastReinforcedAt.IsZero() {
		item.LastReinforcedAt = item.LastAccessedAt
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Item{}, fmt.Errorf("create memories dir: %w", err)
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return Item{}, fmt.Errorf("marshal memory %q: %w", item.ID, err)
	}
	if err := os.WriteFile(s.pathFor(item.ID), data, 0o644); err != nil {
		return Item{}, fmt.Errorf("write memory %q: %w", item.ID, err)
	}
	return item, nil
}

func (s *FileStore) Remember(ctx context.Context, text string, sourceSessionID string) (Item, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Item{}, false, nil
	}
	if LooksSensitive(text) {
		return Item{}, false, nil
	}
	items, err := s.List(ctx)
	if err != nil {
		return Item{}, false, err
	}
	normalized := normalizeText(text)
	for _, item := range items {
		if normalizeText(item.Text) != normalized {
			continue
		}
		item.AccessCount++
		now := time.Now()
		item.LastAccessedAt = now
		item.LastReinforcedAt = now
		if item.Importance < 0.95 {
			item.Importance += 0.05
		}
		if item.RetentionScore < 0.95 {
			item.RetentionScore += 0.05
		}
		updated, err := s.Save(ctx, item)
		return updated, false, err
	}
	item := NewItem(text, TierLongTerm, sourceSessionID)
	created, err := s.Save(ctx, item)
	return created, true, err
}

func (s *FileStore) Touch(ctx context.Context, item Item) error {
	now := time.Now()
	item.AccessCount++
	item.LastAccessedAt = now
	item.LastReinforcedAt = now
	_, err := s.Save(ctx, item)
	return err
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.Remove(s.pathFor(id)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete memory %q: %w", id, err)
	}
	return nil
}

func (s *FileStore) pathFor(id string) string {
	return filepath.Join(s.dir, sanitizeID(id)+".json")
}

func Retention(initial, lambda float64, age time.Duration, strength int) float64 {
	if initial <= 0 {
		initial = 1
	}
	if lambda <= 0 {
		lambda = 0.1
	}
	if strength < 1 {
		strength = 1
	}
	hours := age.Hours()
	return initial * math.Exp(-lambda*hours/float64(strength))
}

func EvolvedImportance(item Item, now time.Time, lambda, alpha float64) float64 {
	if now.IsZero() {
		now = time.Now()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	retention := Retention(1, lambda, now.Sub(item.CreatedAt), item.AccessCount+1)
	if alpha <= 0 {
		alpha = 0.1
	}
	base := item.Importance
	if base <= 0 {
		base = 0.5
	}
	return base * retention * (1 + alpha*float64(item.AccessCount))
}

type KeywordRetriever struct {
	Items []Item
}

func (r KeywordRetriever) Retrieve(query string, limit int) []Item {
	if limit <= 0 {
		limit = 8
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	now := time.Now()
	type scoredItem struct {
		item  Item
		score float64
	}
	scored := make([]scoredItem, 0, len(r.Items))
	for _, item := range r.Items {
		text := strings.ToLower(item.Text + " " + strings.Join(item.Tags, " "))
		matches := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				matches++
			}
		}
		if matches == 0 {
			continue
		}
		score := float64(matches)*10 + EvolvedImportance(item, now, 0.05, 0.1)
		if !item.LastAccessedAt.IsZero() {
			score += 1 / (1 + now.Sub(item.LastAccessedAt).Hours()/24)
		}
		scored = append(scored, scoredItem{item: item, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].item.UpdatedAt.After(scored[j].item.UpdatedAt)
		}
		return scored[i].score > scored[j].score
	})
	out := make([]Item, 0, min(limit, len(scored)))
	for i := 0; i < len(scored) && i < limit; i++ {
		out = append(out, scored[i].item)
	}
	return out
}

func ExplicitMemoryFromPrompt(prompt string) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", false
	}
	patterns := []string{
		`(?is)\bremember\s+(?:that\s+)?(.+)`,
		`(?is)\bplease\s+remember\s+(?:that\s+)?(.+)`,
		`(?is)记住[:：]?\s*(.+)`,
		`(?is)请记住[:：]?\s*(.+)`,
		`(?is)帮我记住[:：]?\s*(.+)`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(prompt)
		if len(matches) < 2 {
			continue
		}
		memory := cleanExtractedMemory(matches[1])
		if memory == "" || LooksSensitive(memory) {
			return "", false
		}
		return memory, true
	}
	return "", false
}

func LooksSensitive(text string) bool {
	lower := strings.ToLower(text)
	sensitiveMarkers := []string{
		"api_key", "apikey", "secret", "password", "passwd", "token", "bearer ",
		"sk-", "ghp_", "github_pat_", "anthropic_api_key", "openai_api_key",
	}
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	longSecret := regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
	return longSecret.MatchString(text)
}

func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool, len(fields))
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len([]rune(field)) < 2 || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func cleanExtractedMemory(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, " \t\r\n。.;；")
	if idx := strings.IndexAny(text, "\n"); idx >= 0 {
		text = strings.TrimSpace(text[:idx])
	}
	return text
}

func stableID(text string) string {
	sum := sha256.Sum256([]byte(normalizeText(text)))
	return "mem_" + hex.EncodeToString(sum[:])[:16]
}

func sanitizeID(id string) string {
	value := strings.TrimSpace(id)
	if value == "" {
		return stableID(time.Now().String())
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
		return stableID(value)
	}
	return b.String()
}

func normalizeText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
}
