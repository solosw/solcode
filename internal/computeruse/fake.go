package computeruse

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
)

// FakeDriver records automation calls for tests without touching a real display.
type FakeDriver struct {
	mu sync.Mutex

	Width  int
	Height int
	Img    image.Image

	Moves   [][2]int
	Clicks  []FakeClick
	Drags   []FakeDrag
	Scrolls []FakeScroll
	Typed   []string
	Keys    []FakeKey
}

type FakeClick struct {
	Button string
	X, Y   int
	Double bool
}

type FakeDrag struct {
	FromX, FromY, ToX, ToY int
}

type FakeScroll struct {
	X, Y, DX, DY int
}

type FakeKey struct {
	Key       string
	Modifiers []string
}

// NewFakeDriver returns a driver with a solid-color screen.
func NewFakeDriver(width, height int) *FakeDriver {
	if width <= 0 {
		width = 800
	}
	if height <= 0 {
		height = 600
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 32, G: 64, B: 128, A: 255})
		}
	}
	return &FakeDriver{Width: width, Height: height, Img: img}
}

func (f *FakeDriver) ScreenSize() (int, int, error) {
	if f == nil {
		return 0, 0, fmt.Errorf("fake driver is nil")
	}
	return f.Width, f.Height, nil
}

func (f *FakeDriver) Capture(x, y, width, height int) (image.Image, error) {
	if f == nil || f.Img == nil {
		return nil, fmt.Errorf("fake driver has no image")
	}
	b := f.Img.Bounds()
	if width <= 0 || height <= 0 {
		width = b.Dx()
		height = b.Dy()
		x, y = b.Min.X, b.Min.Y
	}
	r := image.Rect(x, y, x+width, y+height).Intersect(b)
	if r.Empty() {
		return nil, fmt.Errorf("capture region empty")
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		for xx := r.Min.X; xx < r.Max.X; xx++ {
			out.Set(xx-r.Min.X, yy-r.Min.Y, f.Img.At(xx, yy))
		}
	}
	return out, nil
}

func (f *FakeDriver) Move(x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Moves = append(f.Moves, [2]int{x, y})
	return nil
}

func (f *FakeDriver) Click(button string, x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Clicks = append(f.Clicks, FakeClick{Button: NormalizeButton(button), X: x, Y: y})
	return nil
}

func (f *FakeDriver) DoubleClick(button string, x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Clicks = append(f.Clicks, FakeClick{Button: NormalizeButton(button), X: x, Y: y, Double: true})
	return nil
}

func (f *FakeDriver) Drag(fromX, fromY, toX, toY int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Drags = append(f.Drags, FakeDrag{FromX: fromX, FromY: fromY, ToX: toX, ToY: toY})
	return nil
}

func (f *FakeDriver) Scroll(x, y, dx, dy int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scrolls = append(f.Scrolls, FakeScroll{X: x, Y: y, DX: dx, DY: dy})
	return nil
}

func (f *FakeDriver) Type(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Typed = append(f.Typed, text)
	return nil
}

func (f *FakeDriver) Key(key string, modifiers []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	mods := append([]string(nil), modifiers...)
	for i := range mods {
		mods[i] = strings.ToLower(strings.TrimSpace(mods[i]))
	}
	f.Keys = append(f.Keys, FakeKey{Key: strings.ToLower(strings.TrimSpace(key)), Modifiers: mods})
	return nil
}
