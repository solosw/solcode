package unit_tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/session"
)

func TestSessionUsageStatsPersist(t *testing.T) {
	dir := t.TempDir()
	store := session.NewFileStore(dir)
	s := session.NewSession(session.SessionID("main"), dir, "test-model")
	s.Metadata.Usage.Add(100, 20, 30, 40)
	s.Metadata.Usage.Add(10, 5, 0, 15)

	if err := store.Save(t.Context(), s); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Raw JSON should contain usage fields.
	raw, err := os.ReadFile(filepath.Join(dir, "main.json"))
	if err != nil {
		// try alternate naming
		entries, _ := os.ReadDir(dir)
		if len(entries) == 0 {
			t.Fatalf("read session file: %v", err)
		}
		raw, err = os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("read session file: %v", err)
		}
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid json")
	}
	if !containsAll(string(raw), []string{`"input_tokens"`, `"output_tokens"`, `"cache_read_input_tokens"`}) {
		t.Fatalf("persisted json missing usage fields: %s", string(raw))
	}

	loaded, err := store.Load(t.Context(), session.SessionID("main"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Metadata.Usage.InputTokens != 110 {
		t.Fatalf("InputTokens = %d, want 110", loaded.Metadata.Usage.InputTokens)
	}
	if loaded.Metadata.Usage.OutputTokens != 25 {
		t.Fatalf("OutputTokens = %d, want 25", loaded.Metadata.Usage.OutputTokens)
	}
	if loaded.Metadata.Usage.CacheCreationInputTokens != 30 {
		t.Fatalf("CacheCreation = %d, want 30", loaded.Metadata.Usage.CacheCreationInputTokens)
	}
	if loaded.Metadata.Usage.CacheReadInputTokens != 55 {
		t.Fatalf("CacheRead = %d, want 55", loaded.Metadata.Usage.CacheReadInputTokens)
	}
}

func containsAll(s string, parts []string) bool {
	for _, p := range parts {
		if !containsStr(s, p) {
			return false
		}
	}
	return true
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
