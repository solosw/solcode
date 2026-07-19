package unit_tests

import (
	"runtime"
	"strings"
	"testing"

	internalmcp "github.com/solosw/solcode/internal/mcp"
)

func TestMergeProcessEnvOverridesParent(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"GODOT_PATH=old-value",
		"HOME=/home/user",
	}
	extra := map[string]string{
		"GODOT_PATH": `C:\software\Godot_v4.6.2-stable_mono_win64`,
		"DEBUG":      "true",
	}
	merged := internalmcp.MergeProcessEnvForTest(parent, extra)

	got := envMap(merged)
	if got["GODOT_PATH"] != extra["GODOT_PATH"] {
		t.Fatalf("GODOT_PATH = %q, want config override %q", got["GODOT_PATH"], extra["GODOT_PATH"])
	}
	if got["DEBUG"] != "true" {
		t.Fatalf("DEBUG = %q, want true", got["DEBUG"])
	}
	if got["PATH"] != "/usr/bin" {
		t.Fatalf("PATH should be preserved, got %q", got["PATH"])
	}
	if got["HOME"] != "/home/user" {
		t.Fatalf("HOME should be preserved, got %q", got["HOME"])
	}
	// No duplicate GODOT_PATH entries.
	count := 0
	for _, entry := range merged {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if runtime.GOOS == "windows" {
			if strings.EqualFold(key, "GODOT_PATH") {
				count++
			}
		} else if key == "GODOT_PATH" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected single GODOT_PATH entry, got %d in %#v", count, merged)
	}
}

func TestMergeProcessEnvWindowsCaseInsensitiveOverride(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only case folding")
	}
	parent := []string{"Godot_Path=old", "Path=C:\\Windows"}
	extra := map[string]string{"GODOT_PATH": "new-godot", "PATH": "C:\\custom"}
	merged := internalmcp.MergeProcessEnvForTest(parent, extra)
	got := envMap(merged)
	// Lookup is case-insensitive on Windows via EqualFold in envMap below.
	if !strings.EqualFold(lookupEnv(got, "GODOT_PATH"), "new-godot") {
		t.Fatalf("GODOT_PATH override failed: %#v", got)
	}
	if !strings.EqualFold(lookupEnv(got, "PATH"), "C:\\custom") {
		t.Fatalf("PATH override failed: %#v", got)
	}
}

func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func lookupEnv(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
