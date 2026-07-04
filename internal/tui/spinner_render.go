package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func renderSpinnerLabel(t Theme, frameIndex int, status string, started time.Time) string {
	frame := SpinnerFrames[frameIndex%len(SpinnerFrames)]
	label := strings.TrimSpace(status)
	if label == "" {
		label = "Thinking…"
	}
	if !started.IsZero() {
		label += " " + elapsedLabel(time.Since(started))
	}
	return t.Assistant.Render(frame) + " " + shimmerText(label, t, frameIndex)
}

func elapsedLabel(elapsed time.Duration) string {
	seconds := int(elapsed.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func shimmerText(text string, t Theme, offset int) string {
	base, okBase := parseHexColor(string(t.Claude))
	shine, okShine := parseHexColor(string(t.ClaudeShimmer))
	if !okBase || !okShine || text == "" {
		return t.Assistant.Render(text)
	}
	var b strings.Builder
	for i, r := range text {
		mix := 0.25
		switch (i + offset) % 5 {
		case 0:
			mix = 0.85
		case 1, 4:
			mix = 0.55
		}
		color := interpolateHex(base, shine, mix)
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(string(r)))
	}
	return b.String()
}

type rgb struct {
	r int
	g int
	b int
}

func parseHexColor(value string) (rgb, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 {
		return rgb{}, false
	}
	r, errR := strconv.ParseInt(value[0:2], 16, 0)
	g, errG := strconv.ParseInt(value[2:4], 16, 0)
	b, errB := strconv.ParseInt(value[4:6], 16, 0)
	if errR != nil || errG != nil || errB != nil {
		return rgb{}, false
	}
	return rgb{r: int(r), g: int(g), b: int(b)}, true
}

func interpolateHex(from, to rgb, mix float64) string {
	if mix < 0 {
		mix = 0
	}
	if mix > 1 {
		mix = 1
	}
	r := int(float64(from.r)*(1-mix) + float64(to.r)*mix)
	g := int(float64(from.g)*(1-mix) + float64(to.g)*mix)
	b := int(float64(from.b)*(1-mix) + float64(to.b)*mix)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
