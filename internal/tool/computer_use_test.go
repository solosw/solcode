package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/computeruse"
)

func TestComputerUseScreenshotAndClick(t *testing.T) {
	fake := computeruse.NewFakeDriver(640, 480)
	tool := NewComputerUseTool(fake)
	uctx := &UseContext{WorkDir: t.TempDir()}

	if !tool.IsReadOnly(json.RawMessage(`{"action":"screenshot"}`)) {
		t.Fatal("screenshot should be read-only")
	}
	if tool.IsReadOnly(json.RawMessage(`{"action":"click","x":1,"y":2}`)) {
		t.Fatal("click should not be read-only")
	}
	if !tool.IsDestructive(json.RawMessage(`{"action":"type","text":"hi"}`)) {
		t.Fatal("type should be destructive")
	}

	res, err := tool.Invoke(context.Background(), uctx, json.RawMessage(`{"action":"screen_info"}`))
	if err != nil || res.IsError {
		t.Fatalf("screen_info: %#v err=%v", res, err)
	}
	if !strings.Contains(res.Text, "640x480") {
		t.Fatalf("screen_info text = %q", res.Text)
	}

	res, err = tool.Invoke(context.Background(), uctx, json.RawMessage(`{"action":"screenshot"}`))
	if err != nil || res.IsError {
		t.Fatalf("screenshot: %#v err=%v", res, err)
	}
	if res.Type != "image" || res.Data == "" {
		t.Fatalf("expected image result, got %#v", res)
	}

	res, err = tool.Invoke(context.Background(), uctx, json.RawMessage(`{"action":"click","x":10,"y":20}`))
	if err != nil || res.IsError {
		t.Fatalf("click: %#v err=%v", res, err)
	}
	if len(fake.Clicks) != 1 || fake.Clicks[0].X != 10 || fake.Clicks[0].Y != 20 {
		t.Fatalf("clicks = %#v", fake.Clicks)
	}

	save := filepath.ToSlash(filepath.Join("shot.png"))
	res, err = tool.Invoke(context.Background(), uctx, json.RawMessage(`{"action":"screenshot","save_path":"`+save+`"}`))
	if err != nil || res.IsError {
		t.Fatalf("screenshot save: %#v err=%v", res, err)
	}
	if !strings.Contains(res.Text, "path:") {
		t.Fatalf("caption missing path: %q", res.Text)
	}
}

func TestComputerUseRequiresCoordinates(t *testing.T) {
	tool := NewComputerUseTool(computeruse.NewFakeDriver(100, 100))
	res, err := tool.Invoke(context.Background(), &UseContext{WorkDir: t.TempDir()}, json.RawMessage(`{"action":"click"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error for missing x/y")
	}
}
