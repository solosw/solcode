package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/tool"
)

type timeoutTestTool struct{ name string }

func (t timeoutTestTool) Name() string              { return t.name }
func (timeoutTestTool) Description() string         { return "timeout test tool" }
func (timeoutTestTool) InputSchema() map[string]any { return nil }
func (timeoutTestTool) Invoke(context.Context, *tool.UseContext, json.RawMessage) (*tool.ContentBlock, error) {
	return tool.Result("ok"), nil
}
func (timeoutTestTool) IsDestructive(json.RawMessage) bool     { return false }
func (timeoutTestTool) IsReadOnly(json.RawMessage) bool        { return true }
func (timeoutTestTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (timeoutTestTool) Aliases() []string                      { return nil }
func (timeoutTestTool) ValidateInput(context.Context, json.RawMessage) error {
	return nil
}

func TestTimeoutForTaskToolIsThirtyMinutes(t *testing.T) {
	if got := timeoutForTool(timeoutTestTool{name: tool.TaskToolName}); got != 30*time.Minute {
		t.Fatalf("Task timeout = %s, want 30m", got)
	}
	if got := timeoutForTool(timeoutTestTool{name: tool.SubagentToolName}); got != 30*time.Minute {
		t.Fatalf("Subagent timeout = %s, want 30m", got)
	}
}

func TestTimeoutForBashAndWaitAllowTwentyFourHours(t *testing.T) {
	want := 24*time.Hour + 30*time.Second
	if got := timeoutForTool(timeoutTestTool{name: tool.BashToolName}); got != want {
		t.Fatalf("Bash timeout = %s, want %s", got, want)
	}
	if got := timeoutForTool(timeoutTestTool{name: tool.WaitToolName}); got != want {
		t.Fatalf("Wait timeout = %s, want %s", got, want)
	}
}

func TestTimeoutForAskUserAllowsDialogPlusGrace(t *testing.T) {
	want := time.Duration(tool.AskUserTimeout)*time.Second + 30*time.Second
	if got := timeoutForTool(timeoutTestTool{name: tool.AskUserToolName}); got != want {
		t.Fatalf("AskUser timeout = %s, want %s", got, want)
	}
}

func TestTimeoutForRegularToolIsTwoMinutes(t *testing.T) {
	if got := timeoutForTool(timeoutTestTool{name: "Other"}); got != 2*time.Minute {
		t.Fatalf("regular tool timeout = %s, want 2m", got)
	}
}

type fingerprintBashTool struct {
	mutate func(workDir string) error
}

func (fingerprintBashTool) Name() string                { return tool.BashToolName }
func (fingerprintBashTool) Description() string         { return "fingerprint bash stub" }
func (fingerprintBashTool) InputSchema() map[string]any { return nil }
func (t fingerprintBashTool) Invoke(_ context.Context, uctx *tool.UseContext, _ json.RawMessage) (*tool.ContentBlock, error) {
	if uctx == nil {
		return tool.ErrorResult("missing context"), nil
	}
	if t.mutate != nil {
		if err := t.mutate(uctx.WorkDir); err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		return tool.Result("ok"), nil
	}
	if err := os.WriteFile(filepath.Join(uctx.WorkDir, "mutated.txt"), []byte("after"), 0o644); err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if err := os.WriteFile(filepath.Join(uctx.WorkDir, "created.txt"), []byte("new"), 0o644); err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if err := os.Remove(filepath.Join(uctx.WorkDir, "deleted.txt")); err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return tool.Result("ok"), nil
}
func (fingerprintBashTool) IsDestructive(json.RawMessage) bool     { return true }
func (fingerprintBashTool) IsReadOnly(json.RawMessage) bool        { return false }
func (fingerprintBashTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (fingerprintBashTool) Aliases() []string                      { return nil }
func (fingerprintBashTool) ValidateInput(context.Context, json.RawMessage) error {
	return nil
}

func TestExecuteFingerprintsBashMutations(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "mutated.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "deleted.txt"), []byte("gone"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "unchanged.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}

	captured := map[string]*string{}
	reg := tool.NewRegistry()
	reg.Register(fingerprintBashTool{})
	x := NewToolExecutor(reg, nil)
	result := x.Execute(context.Background(), ToolCall{Name: tool.BashToolName, Input: json.RawMessage(`{}`)}, ToolEnv{
		UseContext: &tool.UseContext{
			WorkDir: work,
			CaptureCheckpoint: func(path string, content *string) {
				rel, err := filepath.Rel(work, path)
				if err != nil {
					rel = path
				}
				rel = filepath.ToSlash(rel)
				if _, ok := captured[rel]; ok {
					return
				}
				captured[rel] = content
			},
		},
	})
	if result.IsError {
		t.Fatalf("execute error: %#v", result.Content)
	}

	if content, ok := captured["mutated.txt"]; !ok || content == nil || *content != "before" {
		t.Fatalf("mutated capture = %#v ok=%v", content, ok)
	}
	if content, ok := captured["created.txt"]; !ok || content != nil {
		t.Fatalf("created capture = %#v ok=%v", content, ok)
	}
	if content, ok := captured["deleted.txt"]; !ok || content == nil || *content != "gone" {
		t.Fatalf("deleted capture = %#v ok=%v", content, ok)
	}
	if _, ok := captured["unchanged.txt"]; ok {
		t.Fatal("unchanged file should not be captured")
	}
}

func TestExecuteFingerprintsSkipEphemeralCreateDeleteWithBaseline(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "keep.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline, err := tool.SnapshotWorkDir(work, tool.FingerprintOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}

	type captureState struct {
		content *string
	}
	captured := map[string]captureState{}
	var order []string
	reg := tool.NewRegistry()
	reg.Register(fingerprintBashTool{
		mutate: func(workDir string) error {
			if err := os.WriteFile(filepath.Join(workDir, "temp.txt"), []byte("tmp"), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(workDir, "keep.txt"), []byte("v2"), 0o644); err != nil {
				return err
			}
			return nil
		},
	})
	x := NewToolExecutor(reg, nil)
	uctx := &tool.UseContext{
		WorkDir:             work,
		FingerprintBaseline: baseline,
		CaptureCheckpoint: func(path string, content *string) {
			rel, err := filepath.Rel(work, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if _, ok := captured[rel]; ok {
				return
			}
			captured[rel] = captureState{content: content}
			order = append(order, rel)
		},
		UncaptureCheckpoint: func(path string) {
			rel, err := filepath.Rel(work, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			delete(captured, rel)
		},
		ListCheckpointPaths: func() []string {
			out := make([]string, 0, len(captured))
			for path := range captured {
				out = append(out, path)
			}
			return out
		},
	}

	// First Bash: create temp + mutate keep.
	result := x.Execute(context.Background(), ToolCall{Name: tool.BashToolName, Input: json.RawMessage(`{}`)}, ToolEnv{UseContext: uctx})
	if result.IsError {
		t.Fatalf("first execute error: %#v", result.Content)
	}
	if _, ok := captured["temp.txt"]; !ok {
		t.Fatalf("temp should be captured after create, got %#v", captured)
	}
	if content, ok := captured["keep.txt"]; !ok || content.content == nil || *content.content != "v1" {
		t.Fatalf("keep capture = %#v ok=%v", content, ok)
	}

	// Second Bash: delete the temp file.
	reg2 := tool.NewRegistry()
	reg2.Register(fingerprintBashTool{
		mutate: func(workDir string) error {
			return os.Remove(filepath.Join(workDir, "temp.txt"))
		},
	})
	x2 := NewToolExecutor(reg2, nil)
	result = x2.Execute(context.Background(), ToolCall{Name: tool.BashToolName, Input: json.RawMessage(`{}`)}, ToolEnv{UseContext: uctx})
	if result.IsError {
		t.Fatalf("second execute error: %#v", result.Content)
	}
	if _, ok := captured["temp.txt"]; ok {
		t.Fatalf("temp should be uncaptured after delete, got %#v", captured)
	}
	if content, ok := captured["keep.txt"]; !ok || content.content == nil || *content.content != "v1" {
		t.Fatalf("keep should remain captured = %#v ok=%v", content, ok)
	}
}

func TestExecuteFingerprintsSameCallCreateDeleteNetsOut(t *testing.T) {
	work := t.TempDir()
	baseline, err := tool.SnapshotWorkDir(work, tool.FingerprintOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	captured := map[string]*string{}
	reg := tool.NewRegistry()
	reg.Register(fingerprintBashTool{
		mutate: func(workDir string) error {
			path := filepath.Join(workDir, "temp.txt")
			if err := os.WriteFile(path, []byte("tmp"), 0o644); err != nil {
				return err
			}
			return os.Remove(path)
		},
	})
	x := NewToolExecutor(reg, nil)
	result := x.Execute(context.Background(), ToolCall{Name: tool.BashToolName, Input: json.RawMessage(`{}`)}, ToolEnv{
		UseContext: &tool.UseContext{
			WorkDir:             work,
			FingerprintBaseline: baseline,
			CaptureCheckpoint: func(path string, content *string) {
				rel, err := filepath.Rel(work, path)
				if err != nil {
					rel = path
				}
				captured[filepath.ToSlash(rel)] = content
			},
			UncaptureCheckpoint: func(path string) {},
			ListCheckpointPaths: func() []string { return nil },
		},
	})
	if result.IsError {
		t.Fatalf("execute error: %#v", result.Content)
	}
	if len(captured) != 0 {
		t.Fatalf("same-call create/delete should not capture, got %#v", captured)
	}
}
