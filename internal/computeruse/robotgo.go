//go:build computeruse

package computeruse

import (
	"fmt"
	"image"
	"strings"

	"github.com/go-vgo/robotgo"
)

// RobotgoDriver implements Driver via github.com/go-vgo/robotgo.
// Coordinates are relative to the primary display. Requires CGO and
// `-tags computeruse` at build time.
type RobotgoDriver struct{}

// NewRobotgoDriver returns the production desktop automation driver.
func NewRobotgoDriver() Driver {
	return RobotgoDriver{}
}

func (RobotgoDriver) ScreenSize() (int, int, error) {
	w, h := robotgo.GetScreenSize()
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("invalid screen size %dx%d", w, h)
	}
	return w, h, nil
}

func (RobotgoDriver) Capture(x, y, width, height int) (image.Image, error) {
	var (
		img    image.Image
		err    error
		region string
	)
	if width <= 0 || height <= 0 {
		img, err = robotgo.CaptureImg()
	} else {
		img, err = robotgo.CaptureImg(x, y, width, height)
		region = fmt.Sprintf(" for region %d,%d %dx%d", x, y, width, height)
	}
	if err != nil {
		return nil, fmt.Errorf("screenshot capture failed%s: %w", region, err)
	}
	if img == nil {
		return nil, fmt.Errorf("screenshot capture failed%s", region)
	}
	return img, nil
}

func (RobotgoDriver) Move(x, y int) error {
	robotgo.Move(x, y)
	return nil
}

func (d RobotgoDriver) Click(button string, x, y int) error {
	btn := robotgoButton(button)
	if x >= 0 && y >= 0 {
		if err := d.Move(x, y); err != nil {
			return err
		}
	}
	return robotgo.Click(btn)
}

func (d RobotgoDriver) DoubleClick(button string, x, y int) error {
	btn := robotgoButton(button)
	if x >= 0 && y >= 0 {
		if err := d.Move(x, y); err != nil {
			return err
		}
	}
	return robotgo.Click(btn, true)
}

func (RobotgoDriver) Drag(fromX, fromY, toX, toY int) error {
	robotgo.Move(fromX, fromY)
	if err := robotgo.Toggle("left"); err != nil {
		return err
	}
	robotgo.MoveSmooth(toX, toY)
	if err := robotgo.Toggle("left", "up"); err != nil {
		return err
	}
	return nil
}

func (d RobotgoDriver) Scroll(x, y, dx, dy int) error {
	if x >= 0 && y >= 0 {
		if err := d.Move(x, y); err != nil {
			return err
		}
	}
	if dx != 0 {
		dir := "right"
		if dx < 0 {
			dir = "left"
			dx = -dx
		}
		robotgo.ScrollDir(dx, dir)
	}
	if dy != 0 {
		dir := "down"
		if dy < 0 {
			dir = "up"
			dy = -dy
		}
		robotgo.ScrollDir(dy, dir)
	}
	return nil
}

func (RobotgoDriver) Type(text string) error {
	if text == "" {
		return fmt.Errorf("text is required")
	}
	robotgo.TypeStr(text)
	return nil
}

func (RobotgoDriver) Key(key string, modifiers []string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("key is required")
	}
	args := make([]interface{}, 0, len(modifiers)+1)
	for _, mod := range modifiers {
		mod = strings.TrimSpace(mod)
		if mod == "" {
			continue
		}
		args = append(args, strings.ToLower(mod))
	}
	if err := robotgo.KeyTap(key, args...); err != nil {
		return err
	}
	return nil
}

func robotgoButton(button string) string {
	switch NormalizeButton(button) {
	case "right":
		return "right"
	case "center":
		return "center"
	default:
		return "left"
	}
}
