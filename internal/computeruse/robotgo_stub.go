//go:build !computeruse

package computeruse

import (
	"fmt"
	"image"
)

// unsupportedDriver is compiled when the binary is built without -tags computeruse.
type unsupportedDriver struct{}

// NewRobotgoDriver returns a driver that errors until the binary is rebuilt with
// CGO and -tags computeruse (links github.com/go-vgo/robotgo).
func NewRobotgoDriver() Driver {
	return unsupportedDriver{}
}

func (unsupportedDriver) ScreenSize() (int, int, error) {
	return 0, 0, errComputerUseBuildTag
}

func (unsupportedDriver) Capture(int, int, int, int) (image.Image, error) {
	return nil, errComputerUseBuildTag
}

func (unsupportedDriver) Move(int, int) error { return errComputerUseBuildTag }

func (unsupportedDriver) Click(string, int, int) error { return errComputerUseBuildTag }

func (unsupportedDriver) DoubleClick(string, int, int) error { return errComputerUseBuildTag }

func (unsupportedDriver) Drag(int, int, int, int) error { return errComputerUseBuildTag }

func (unsupportedDriver) Scroll(int, int, int, int) error { return errComputerUseBuildTag }

func (unsupportedDriver) Type(string) error { return errComputerUseBuildTag }

func (unsupportedDriver) Key(string, []string) error { return errComputerUseBuildTag }

var errComputerUseBuildTag = fmt.Errorf("ComputerUse requires rebuilding solcode with CGO_ENABLED=1 -tags computeruse (robotgo)")
