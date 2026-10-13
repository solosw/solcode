package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBatchSettingsLoadAndPartialOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"batch":{"max_operations":6,"max_parallel":3,"max_result_runes":4000,"max_total_result_runes":9000,"max_read_retries":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := BatchConfig{MaxOperations: 6, MaxParallel: 3, MaxResultRunes: 4000, MaxTotalResultRunes: 9000, MaxReadRetries: 0}
	if cfg.Batch != expected {
		t.Fatalf("batch settings ignored: got=%+v want=%+v", cfg.Batch, expected)
	}
	overlay := filepath.Join(dir, "settings.local.json")
	if err := os.WriteFile(overlay, []byte(`{"batch":{"max_parallel":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadFile(&cfg, overlay); err != nil {
		t.Fatal(err)
	}
	expected.MaxParallel = 2
	if cfg.Batch != expected {
		t.Fatalf("partial batch overlay erased unrelated values: %+v", cfg.Batch)
	}
}

func TestBatchReadRetryDefaultCanBeDisabled(t *testing.T) {
	if got := Default().Batch.MaxReadRetries; got != 1 {
		t.Fatalf("default read retries=%d", got)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"batch":{"max_total_result_runes":1000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Batch.MaxReadRetries != 1 || cfg.Batch.MaxTotalResultRunes != 1000 {
		t.Fatalf("partial config=%+v", cfg.Batch)
	}
}
