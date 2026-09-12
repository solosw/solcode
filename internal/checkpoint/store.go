package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store persists per-session file snapshots beside the session JSON.
type Store struct {
	root        string
	workDir     string
	retainTurns int

	mu         sync.Mutex
	activeTurn int
	hasActive  bool
	captured   map[string]bool
}

type turnMeta struct {
	Version int        `json:"version"`
	Turn    int        `json:"turn"`
	Time    time.Time  `json:"time"`
	Prompt  string     `json:"prompt"`
	Name    string     `json:"name,omitempty"`
	Files   []fileMeta `json:"files"`
}

type fileMeta struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Index   int    `json:"index"`
}

// DirForSession returns the sidecar directory for a session id.
func DirForSession(sessionDir, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	return filepath.Join(sessionDir, sessionID+".ckpt")
}

// NewStore opens (or creates) a checkpoint store rooted under sessionDir.
func NewStore(sessionDir, sessionID, workDir string, retainTurns int) (*Store, error) {
	workDir = filepath.Clean(strings.TrimSpace(workDir))
	if workDir == "" {
		return nil, fmt.Errorf("workDir is required")
	}
	if retainTurns <= 0 {
		retainTurns = DefaultRetainTurns
	}
	root := DirForSession(sessionDir, sessionID)
	if err := os.MkdirAll(filepath.Join(root, "turns"), 0o755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir: %w", err)
	}
	return &Store{
		root:        root,
		workDir:     workDir,
		retainTurns: retainTurns,
		captured:    map[string]bool{},
	}, nil
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// BeginTurn opens a new checkpoint for the next user turn.
func (s *Store) BeginTurn(prompt string) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("checkpoint store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := 0
	if turns, err := s.listTurnNumbersLocked(); err == nil && len(turns) > 0 {
		next = turns[len(turns)-1] + 1
	}
	meta := turnMeta{
		Version: SchemaVersion,
		Turn:    next,
		Time:    time.Now().UTC(),
		Prompt:  strings.TrimSpace(prompt),
		Files:   []fileMeta{},
	}
	if err := s.writeTurnMetaLocked(meta); err != nil {
		return 0, err
	}
	s.activeTurn = next
	s.hasActive = true
	s.captured = map[string]bool{}
	_ = s.pruneLocked()
	return next, nil
}

// Capture records the turn-start content for path once per active turn.
// content == nil means the file did not exist.
func (s *Store) Capture(relOrAbsPath string, content *string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasActive {
		return nil
	}
	rel, err := s.relPathLocked(relOrAbsPath)
	if err != nil {
		return err
	}
	key := filepath.ToSlash(rel)
	if s.captured[key] {
		return nil
	}
	meta, err := s.readTurnMetaLocked(s.activeTurn)
	if err != nil {
		return err
	}
	idx := len(meta.Files)
	if err := s.writeFilePayloadLocked(s.activeTurn, idx, content); err != nil {
		return err
	}
	meta.Files = append(meta.Files, fileMeta{
		Path:    key,
		Existed: content != nil,
		Index:   idx,
	})
	if err := s.writeTurnMetaLocked(meta); err != nil {
		return err
	}
	s.captured[key] = true
	return nil
}

// List returns checkpoint metadata newest-last.
func (s *Store) List() ([]Meta, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	turns, err := s.listTurnNumbersLocked()
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(turns))
	for _, turn := range turns {
		meta, err := s.readTurnMetaLocked(turn)
		if err != nil {
			continue
		}
		out = append(out, Meta{
			Turn:      meta.Turn,
			Time:      meta.Time,
			Prompt:    meta.Prompt,
			Name:      meta.Name,
			FileCount: len(meta.Files),
		})
	}
	return out, nil
}

// Load returns one checkpoint including file payloads.
func (s *Store) Load(turn int) (*Checkpoint, error) {
	if s == nil {
		return nil, fmt.Errorf("checkpoint store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.readTurnMetaLocked(turn)
	if err != nil {
		return nil, err
	}
	cp := &Checkpoint{
		Turn:   meta.Turn,
		Time:   meta.Time,
		Prompt: meta.Prompt,
		Name:   meta.Name,
		Files:  make([]FileSnap, 0, len(meta.Files)),
	}
	for _, f := range meta.Files {
		snap := FileSnap{Path: f.Path}
		if f.Existed {
			body, err := s.readFilePayloadLocked(turn, f.Index)
			if err != nil {
				return nil, err
			}
			text := string(body)
			snap.Content = &text
		}
		cp.Files = append(cp.Files, snap)
	}
	return cp, nil
}

// NormalizeCheckpointName trims and validates a user-facing checkpoint label.
func NormalizeCheckpointName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("checkpoint name is required")
	}
	if _, err := strconv.Atoi(name); err == nil {
		return "", fmt.Errorf("checkpoint name %q looks like a turn number; use /rewind <turn> instead", name)
	}
	lower := strings.ToLower(name)
	switch lower {
	case "save", "name", "list", "checkpoints", "checkpoint-name", "rewind":
		return "", fmt.Errorf("checkpoint name %q is reserved", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			// ok
		case r == '-' || r == '_' || r == '.':
			if i == 0 {
				return "", fmt.Errorf("checkpoint name %q must start with a letter or digit", name)
			}
		default:
			return "", fmt.Errorf("checkpoint name %q may only contain letters, digits, '-', '_' and '.'", name)
		}
	}
	if len([]rune(name)) > 48 {
		return "", fmt.Errorf("checkpoint name is too long (max 48 characters)")
	}
	return name, nil
}

// SetName labels a checkpoint. Names are unique within a session (case-insensitive).
// turn < 0 means the newest checkpoint.
func (s *Store) SetName(turn int, name string) (Meta, error) {
	var out Meta
	if s == nil {
		return out, fmt.Errorf("checkpoint store is nil")
	}
	name, err := NormalizeCheckpointName(name)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	turns, err := s.listTurnNumbersLocked()
	if err != nil {
		return out, err
	}
	if len(turns) == 0 {
		return out, fmt.Errorf("no checkpoints")
	}
	if turn < 0 {
		turn = turns[len(turns)-1]
	}
	found := false
	for _, t := range turns {
		if t == turn {
			found = true
			break
		}
	}
	if !found {
		return out, fmt.Errorf("checkpoint turn %d not found", turn)
	}

	want := strings.ToLower(name)
	for _, t := range turns {
		meta, err := s.readTurnMetaLocked(t)
		if err != nil {
			continue
		}
		if t == turn {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(meta.Name), want) {
			return out, fmt.Errorf("checkpoint name %q already used by turn %d", name, t)
		}
	}

	meta, err := s.readTurnMetaLocked(turn)
	if err != nil {
		return out, err
	}
	meta.Name = name
	if err := s.writeTurnMetaLocked(meta); err != nil {
		return out, err
	}
	return Meta{
		Turn:      meta.Turn,
		Time:      meta.Time,
		Prompt:    meta.Prompt,
		Name:      meta.Name,
		FileCount: len(meta.Files),
	}, nil
}

// FindTurnByName resolves a checkpoint label to its turn number (case-insensitive).
func (s *Store) FindTurnByName(name string) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("checkpoint store is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("checkpoint name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	turns, err := s.listTurnNumbersLocked()
	if err != nil {
		return 0, err
	}
	want := strings.ToLower(name)
	for _, t := range turns {
		meta, err := s.readTurnMetaLocked(t)
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(meta.Name), want) {
			return t, nil
		}
	}
	return 0, fmt.Errorf("checkpoint name %q not found", name)
}

// RestoreFiles restores workspace files to the state at the start of targetTurn
// by applying the earliest snap per path across turns [targetTurn, latest].
func (s *Store) RestoreFiles(targetTurn int) (RestoreResult, error) {
	var result RestoreResult
	if s == nil {
		return result, fmt.Errorf("checkpoint store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	turns, err := s.listTurnNumbersLocked()
	if err != nil {
		return result, err
	}
	if len(turns) == 0 {
		return result, fmt.Errorf("no checkpoints")
	}
	found := false
	for _, t := range turns {
		if t == targetTurn {
			found = true
			break
		}
	}
	if !found {
		return result, fmt.Errorf("checkpoint turn %d not found", targetTurn)
	}

	earliest := map[string]FileSnap{}
	order := make([]string, 0)
	for _, t := range turns {
		if t < targetTurn {
			continue
		}
		meta, err := s.readTurnMetaLocked(t)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		for _, f := range meta.Files {
			if _, ok := earliest[f.Path]; ok {
				continue
			}
			snap := FileSnap{Path: f.Path}
			if f.Existed {
				body, err := s.readFilePayloadLocked(t, f.Index)
				if err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", f.Path, err))
					continue
				}
				text := string(body)
				snap.Content = &text
			}
			earliest[f.Path] = snap
			order = append(order, f.Path)
		}
	}

	for _, path := range order {
		snap := earliest[path]
		abs, err := s.absPathLocked(snap.Path)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		if snap.Content == nil {
			err := os.Remove(abs)
			if err != nil && !os.IsNotExist(err) {
				result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", snap.Path, err))
				continue
			}
			if err == nil {
				result.Deleted = append(result.Deleted, snap.Path)
			} else {
				result.Skipped = append(result.Skipped, snap.Path+" (already absent)")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("mkdir %s: %v", snap.Path, err))
			continue
		}
		if err := os.WriteFile(abs, []byte(*snap.Content), 0o644); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("write %s: %v", snap.Path, err))
			continue
		}
		result.Restored = append(result.Restored, snap.Path)
	}
	return result, nil
}

func (s *Store) pruneLocked() error {
	turns, err := s.listTurnNumbersLocked()
	if err != nil {
		return err
	}
	if len(turns) <= s.retainTurns {
		return nil
	}
	drop := turns[:len(turns)-s.retainTurns]
	for _, t := range drop {
		_ = os.RemoveAll(s.turnDir(t))
	}
	return nil
}

func (s *Store) listTurnNumbersLocked() ([]int, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "turns"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.turnDir(n), "meta.json")); err != nil {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

func (s *Store) turnDir(turn int) string {
	return filepath.Join(s.root, "turns", fmt.Sprintf("%04d", turn))
}

func (s *Store) writeTurnMetaLocked(meta turnMeta) error {
	dir := s.turnDir(meta.Turn)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644)
}

func (s *Store) readTurnMetaLocked(turn int) (turnMeta, error) {
	var meta turnMeta
	data, err := os.ReadFile(filepath.Join(s.turnDir(turn), "meta.json"))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func (s *Store) writeFilePayloadLocked(turn, index int, content *string) error {
	path := filepath.Join(s.turnDir(turn), "files", fmt.Sprintf("%d.before", index))
	if content == nil {
		return os.WriteFile(path+".missing", []byte("1"), 0o644)
	}
	return os.WriteFile(path, []byte(*content), 0o644)
}

func (s *Store) readFilePayloadLocked(turn, index int) ([]byte, error) {
	path := filepath.Join(s.turnDir(turn), "files", fmt.Sprintf("%d.before", index))
	return os.ReadFile(path)
}

func (s *Store) relPathLocked(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs := path
	if !filepath.IsAbs(path) {
		abs = filepath.Join(s.workDir, path)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(s.workDir, abs)
	if err != nil {
		return "", fmt.Errorf("path %q escapes workdir: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workdir", path)
	}
	return filepath.ToSlash(rel), nil
}

func (s *Store) absPathLocked(rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid relative path %q", rel)
	}
	abs := filepath.Clean(filepath.Join(s.workDir, rel))
	check, err := filepath.Rel(s.workDir, abs)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workdir", rel)
	}
	return abs, nil
}
