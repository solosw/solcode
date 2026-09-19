package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/computeruse"
)

const ComputerUseToolName = "ComputerUse"

// ComputerUseParams is the input schema for desktop automation.
type ComputerUseParams struct {
	Action           string   `json:"action"`
	X                *int     `json:"x,omitempty"`
	Y                *int     `json:"y,omitempty"`
	ToX              *int     `json:"to_x,omitempty"`
	ToY              *int     `json:"to_y,omitempty"`
	Width            *int     `json:"width,omitempty"`
	Height           *int     `json:"height,omitempty"`
	Text             string   `json:"text,omitempty"`
	Key              string   `json:"key,omitempty"`
	Modifiers        []string `json:"modifiers,omitempty"`
	Button           string   `json:"button,omitempty"`
	ScrollDX         *int     `json:"scroll_dx,omitempty"`
	ScrollDY         *int     `json:"scroll_dy,omitempty"`
	ReturnScreenshot bool     `json:"return_screenshot,omitempty"`
	SavePath         string   `json:"save_path,omitempty"`
}

type computerUseTool struct {
	BaseTool
	driver computeruse.Driver
}

// NewComputerUseTool creates a desktop automation tool using the given driver.
func NewComputerUseTool(driver computeruse.Driver) Tool {
	if driver == nil {
		driver = computeruse.NewRobotgoDriver()
	}
	return &computerUseTool{driver: driver}
}

func (t *computerUseTool) Name() string { return ComputerUseToolName }

func (t *computerUseTool) Description() string {
	return `Desktop computer use: screenshot the primary display and control mouse/keyboard.
Use for GUI automation when terminal/file tools are not enough (click buttons, type into apps, verify on-screen state).
Actions: screenshot, screen_info, click, double_click, right_click, move, drag, type, key, scroll.
- Coordinates are pixels on the primary screen (origin top-left).
- Prefer screenshot first, then act, then screenshot again to verify.
- screenshot / screen_info are read-only; other actions are destructive and need permission in auto mode.
- Not a core tool: enable via settings computer_use.enabled, then Skill "computer-use" or ToolSearch.
- Optional return_screenshot after mutating actions; optional save_path under workdir.`
}

func (t *computerUseTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{
					"screenshot", "screen_info", "click", "double_click", "right_click",
					"move", "drag", "type", "key", "scroll",
				},
				"description": "Automation action to perform",
			},
			"x": map[string]any{
				"type":        "integer",
				"description": "X pixel coordinate (required for click/move/drag start/scroll focus)",
			},
			"y": map[string]any{
				"type":        "integer",
				"description": "Y pixel coordinate",
			},
			"to_x": map[string]any{
				"type":        "integer",
				"description": "Drag end X",
			},
			"to_y": map[string]any{
				"type":        "integer",
				"description": "Drag end Y",
			},
			"width": map[string]any{
				"type":        "integer",
				"description": "Optional screenshot region width",
			},
			"height": map[string]any{
				"type":        "integer",
				"description": "Optional screenshot region height",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Text to type (action=type)",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Key name for action=key (e.g. enter, escape, a)",
			},
			"modifiers": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional modifiers for key (ctrl, alt, shift, cmd/win)",
			},
			"button": map[string]any{
				"type":        "string",
				"description": "Mouse button: left (default), right, center",
			},
			"scroll_dx": map[string]any{
				"type":        "integer",
				"description": "Horizontal scroll amount (positive=right)",
			},
			"scroll_dy": map[string]any{
				"type":        "integer",
				"description": "Vertical scroll amount (positive=down)",
			},
			"return_screenshot": map[string]any{
				"type":        "boolean",
				"description": "After a mutating action, also return a screenshot image block",
			},
			"save_path": map[string]any{
				"type":        "string",
				"description": "Optional workdir-relative path to save a PNG of the screenshot",
			},
		},
		"required": []string{"action"},
	}
}

func (t *computerUseTool) IsDestructive(input json.RawMessage) bool {
	return !t.IsReadOnly(input)
}

func (t *computerUseTool) IsReadOnly(input json.RawMessage) bool {
	var params ComputerUseParams
	if json.Unmarshal(input, &params) != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(params.Action)) {
	case "screenshot", "screen_info":
		return true
	default:
		return false
	}
}

func (t *computerUseTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *computerUseTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	_ = ctx
	if t.driver == nil {
		return ErrorResult("computer use driver is not configured"), nil
	}
	var params ComputerUseParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	action := strings.ToLower(strings.TrimSpace(params.Action))
	if action == "" {
		return ErrorResult("action is required"), nil
	}

	switch action {
	case "screen_info":
		w, h, err := t.driver.ScreenSize()
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		return Result(fmt.Sprintf("primary screen: %dx%d", w, h)), nil
	case "screenshot":
		return t.screenshot(uctx, params, "")
	case "click", "double_click", "right_click":
		x, y, err := requireXY(params)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		button := params.Button
		if action == "right_click" {
			button = "right"
		}
		if action == "double_click" {
			err = t.driver.DoubleClick(button, x, y)
		} else {
			err = t.driver.Click(button, x, y)
		}
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		msg := fmt.Sprintf("%s %s at (%d,%d)", action, computeruse.NormalizeButton(button), x, y)
		return t.maybeScreenshot(uctx, params, msg)
	case "move":
		x, y, err := requireXY(params)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		if err := t.driver.Move(x, y); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return t.maybeScreenshot(uctx, params, fmt.Sprintf("moved pointer to (%d,%d)", x, y))
	case "drag":
		x, y, err := requireXY(params)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		if params.ToX == nil || params.ToY == nil {
			return ErrorResult("to_x and to_y are required for drag"), nil
		}
		if err := t.driver.Drag(x, y, *params.ToX, *params.ToY); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return t.maybeScreenshot(uctx, params, fmt.Sprintf("dragged (%d,%d) -> (%d,%d)", x, y, *params.ToX, *params.ToY))
	case "type":
		if strings.TrimSpace(params.Text) == "" {
			return ErrorResult("text is required for type"), nil
		}
		if err := t.driver.Type(params.Text); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return t.maybeScreenshot(uctx, params, fmt.Sprintf("typed %d characters", len(params.Text)))
	case "key":
		if strings.TrimSpace(params.Key) == "" {
			return ErrorResult("key is required"), nil
		}
		if err := t.driver.Key(params.Key, params.Modifiers); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return t.maybeScreenshot(uctx, params, fmt.Sprintf("key %q modifiers=%v", params.Key, params.Modifiers))
	case "scroll":
		x, y := -1, -1
		if params.X != nil && params.Y != nil {
			x, y = *params.X, *params.Y
		}
		dx, dy := 0, 0
		if params.ScrollDX != nil {
			dx = *params.ScrollDX
		}
		if params.ScrollDY != nil {
			dy = *params.ScrollDY
		}
		if dx == 0 && dy == 0 {
			return ErrorResult("scroll_dx and/or scroll_dy is required"), nil
		}
		if err := t.driver.Scroll(x, y, dx, dy); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return t.maybeScreenshot(uctx, params, fmt.Sprintf("scrolled dx=%d dy=%d at (%d,%d)", dx, dy, x, y))
	default:
		return ErrorResult("unknown action: " + params.Action), nil
	}
}

func requireXY(params ComputerUseParams) (int, int, error) {
	if params.X == nil || params.Y == nil {
		return 0, 0, fmt.Errorf("x and y are required")
	}
	return *params.X, *params.Y, nil
}

func (t *computerUseTool) maybeScreenshot(uctx *UseContext, params ComputerUseParams, msg string) (*ContentBlock, error) {
	if !params.ReturnScreenshot {
		return Result(msg), nil
	}
	return t.screenshot(uctx, params, msg)
}

func (t *computerUseTool) screenshot(uctx *UseContext, params ComputerUseParams, captionPrefix string) (*ContentBlock, error) {
	x, y, w, h := 0, 0, 0, 0
	if params.Width != nil && params.Height != nil && *params.Width > 0 && *params.Height > 0 {
		w, h = *params.Width, *params.Height
		if params.X != nil {
			x = *params.X
		}
		if params.Y != nil {
			y = *params.Y
		}
	}
	img, err := t.driver.Capture(x, y, w, h)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	att, err := attach.OptimizeImage(img)
	if err != nil {
		return ErrorResult("optimize screenshot: " + err.Error()), nil
	}

	caption := strings.TrimSpace(captionPrefix)
	if caption != "" {
		caption += "\n"
	}
	saveNote := ""
	if path := strings.TrimSpace(params.SavePath); path != "" {
		abs := ResolvePath(uctx, path)
		if err := CheckAllowedPath(uctx, abs); err != nil {
			return ErrorResult(err.Error()), nil
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return ErrorResult("mkdir save_path: " + err.Error()), nil
		}
		if err := writeBase64Image(abs, att.MimeType, att.Data); err != nil {
			return ErrorResult("save screenshot: " + err.Error()), nil
		}
		saveNote = fmt.Sprintf(" path: %s", abs)
		att.Path = abs
	}
	caption += fmt.Sprintf("screenshot %dx%d (~%d vision tokens)%s", att.Width, att.Height, att.Tokens, saveNote)
	return ImageResult(att.MimeType, att.Data, caption), nil
}

func writeBase64Image(path, mime, data string) error {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	ext := filepath.Ext(path)
	if ext == "" {
		switch mime {
		case "image/jpeg":
			path += ".jpg"
		default:
			path += ".png"
		}
	}
	return os.WriteFile(path, raw, 0o644)
}
