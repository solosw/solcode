// Package computeruse abstracts desktop screen capture and input automation.
package computeruse

import "image"

// Driver performs desktop automation. Implementations may wrap robotgo or a
// test fake; callers must not assume a particular display layout beyond the
// primary screen unless the driver documents otherwise.
type Driver interface {
	ScreenSize() (width, height int, err error)
	Capture(x, y, width, height int) (image.Image, error)
	Move(x, y int) error
	Click(button string, x, y int) error
	DoubleClick(button string, x, y int) error
	Drag(fromX, fromY, toX, toY int) error
	Scroll(x, y, dx, dy int) error
	Type(text string) error
	Key(key string, modifiers []string) error
}

// NormalizeButton maps common button names to left|right|center.
func NormalizeButton(button string) string {
	switch button {
	case "", "left", "l", "1":
		return "left"
	case "right", "r", "3":
		return "right"
	case "center", "middle", "m", "2":
		return "center"
	default:
		return button
	}
}
