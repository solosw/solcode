package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const malformedPTCSettings = "{\n  \"batch\": {\n    \"ptc\": {\"enabled\": true}\n  }\n  \"api_key\": \"must-not-leak-in-errors\"\n}\n"

func TestSettingsSyntaxErrorReportsLocationWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(path, []byte(malformedPTCSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line 5 column 3") {
		t.Fatalf("Load error = %v, want filename and missing-comma location", err)
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("Load error lost the underlying syntax error: %v", err)
	}
	if strings.Contains(err.Error(), "must-not-leak-in-errors") {
		t.Fatal("config error exposed a secret")
	}
	if err := SaveLocalOverrides(path, map[string]any{"effort": "high"}); err == nil || !strings.Contains(err.Error(), "line 5 column 3") {
		t.Fatalf("SaveLocalOverrides error = %v, want malformed config location", err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != malformedPTCSettings {
		t.Fatal("saving over malformed settings must not overwrite them or silently disable PTC")
	}
}

func TestSettingsParseErrorHandlesUnicodeColumnsAndNonSyntaxErrors(t *testing.T) {
	data := []byte(`{"标题":"值" "batch":{}}`)
	var parsed any
	err := json.Unmarshal(data, &parsed)
	located := settingsParseError("parse config", "settings.json", data, err)
	if !strings.Contains(located.Error(), "line 1 column 11") {
		t.Fatalf("unicode location = %v", located)
	}
	cause := errors.New("invalid option type")
	if err := settingsParseError("parse config", "settings.json", data, cause); !errors.Is(err, cause) || strings.Contains(err.Error(), "line ") {
		t.Fatalf("non-syntax error = %v", err)
	}
}

func TestSavingSettingsPreservesPTCAndFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(path, []byte(`{"batch":{"ptc":{"enabled":true},"max_parallel":8}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalOverrides(path, map[string]any{"batch": map[string]any{"max_result_runes": 36000}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.Batch.PTC.Enabled || cfg.Batch.MaxParallel != 8 || cfg.Batch.MaxResultRunes != 36000 {
		t.Fatalf("saved PTC config = %+v, err = %v", cfg.Batch, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("settings permissions widened to %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary settings files leaked: %v, %v", entries, err)
	}
}

func TestSettingsSavesDoNotExposePartialJSONToStartupReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.local.json")
	if err := SaveLocalOverrides(path, map[string]any{"batch": map[string]any{"ptc": map[string]any{"enabled": true}}, "padding": strings.Repeat("x", 64*1024)}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	readErrors := make(chan error, 4)
	var wg, started sync.WaitGroup
	for range 4 {
		wg.Add(1)
		started.Add(1)
		go func() {
			defer wg.Done()
			started.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var cfg Config
				if err := loadFile(&cfg, path); err != nil {
					readErrors <- err
					return
				}
				if !cfg.Batch.PTC.Enabled {
					readErrors <- fmt.Errorf("PTC was lost while reading settings during a save")
					return
				}
			}
		}()
	}
	stopReaders := sync.OnceFunc(func() { close(stop); wg.Wait() })
	defer stopReaders()
	started.Wait()
	for i := range 40 {
		if err := SaveLocalOverrides(path, map[string]any{"revision": i}); err != nil {
			t.Fatal(err)
		}
	}
	stopReaders()
	select {
	case err := <-readErrors:
		t.Fatalf("startup reader observed partial settings: %v", err)
	default:
	}
}

func TestSavingSettingsKeepsExistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges are not available on all Windows hosts")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "managed.json")
	link := filepath.Join(dir, "settings.local.json")
	if err := os.WriteFile(target, []byte(`{"batch":{"ptc":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalOverrides(link, map[string]any{"effort": "high"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings save replaced the symlink")
	}
	cfg, err := Load(target)
	if err != nil || !cfg.Batch.PTC.Enabled || cfg.Effort != "high" {
		t.Fatalf("symlink target = %+v, err = %v", cfg.Batch, err)
	}
}

func TestFailedSettingsReplacementKeepsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.local.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSettingsFile(target, []byte(`{}`)); err == nil {
		t.Fatal("replacing a directory with settings should fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed replacement modified the original target or leaked temp files: %v, %v", entries, err)
	}
}
