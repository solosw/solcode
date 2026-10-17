package bundle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This opt-in smoke test extracts the actual pinned payload. It never clicks,
// types, launches a desktop app, or requests system permissions.
func TestEmbeddedComputerUseRuntime(t *testing.T) {
	if os.Getenv("SOLCODE_VERIFY_NATIVE_BUNDLE") != "1" {
		t.Skip("set SOLCODE_VERIFY_NATIVE_BUNDLE=1 after preparing the native payload")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary, err := Resolve(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(binary, dir+string(filepath.Separator)) {
		t.Fatal("expected embedded payload, not PATH fallback")
	}
	again, err := Resolve(ctx, "", dir)
	if err != nil || again != binary {
		t.Fatalf("cache reuse: %s, %v", again, err)
	}
	if runtime.GOOS == "darwin" {
		app := filepath.Dir(filepath.Dir(filepath.Dir(binary)))
		output, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", app).CombinedOutput()
		if err != nil {
			t.Fatalf("embedded signed .app was damaged: %s, %v", output, err)
		}
	}
	output, err := exec.CommandContext(ctx, binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("native help failed: %s, %v", output, err)
	}
	if !strings.Contains(string(output), "mcp") {
		t.Fatalf("unexpected help output: %s", output)
	}
}
