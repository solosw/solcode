package unit_tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	headroom "github.com/superops-team/headroom-go"

	"github.com/solosw/codeplus-agent/internal/session"
)

func TestHeadroomJSONVsMessageCompact(t *testing.T) {
	ctx := context.Background()

	path := "C:\\software\\codeplus\\codeplus-agent\\session-20260707-165344.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("session file not available: %v", err)
	}
	rawText := string(raw)
	t.Logf("raw file size: %d bytes (%d chars)", len(raw), len(rawText))

	// Step 1: Parse as session.
	var s session.Session
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse session: %v", err)
	}
	t.Logf("parsed session: %d messages, summary=%q", len(s.Messages), s.Summary)

	// Step 2: Current Compact path.
	result, err := session.Compact(ctx, s.Summary, s.Messages, nil, session.CompactOptions{
		MaxRecentTurns:         5,
		SummaryThresholdTokens: 1,
	})
	if err != nil {
		t.Fatalf("session.Compact: %v", err)
	}
	compactJSON, _ := json.MarshalIndent(session.Session{
		Metadata: s.Metadata,
		Messages: result.Messages,
		Summary:  result.Summary,
	}, "", "  ")
	t.Logf("session.Compact output: %d bytes, %d messages", len(compactJSON), len(result.Messages))

	// Verify session.Compact output is parseable
	var check session.Session
	if err := json.Unmarshal(compactJSON, &check); err != nil {
		t.Fatalf("session.Compact output is NOT valid JSON: %v", err)
	}
	t.Logf("session.Compact output: valid JSON, %d messages", len(check.Messages))

	// Step 3: headroom.CompressString on the raw JSON.
	opts := headroom.DefaultOptions()
	opts.Aggressiveness = 0.8
	opts.Reversible = false
	opts.EnablePipeline = true
	opts.TokenBudget = 40000

	compressed, err := headroom.CompressString(rawText, opts)
	if err != nil {
		t.Fatalf("headroom.CompressString(raw JSON): %v", err)
	}
	t.Logf("headroom.CompressString(raw JSON) output: %d bytes (saved %.1f%%)",
		len(compressed), 100*(1-float64(len(compressed))/float64(len(rawText))))

	// Verify headroom output is parseable
	var check2 session.Session
	if err := json.Unmarshal([]byte(compressed), &check2); err != nil {
		t.Logf("headroom.CompressString output is NOT valid JSON: %v", err)
	} else {
		t.Logf("headroom.CompressString output: valid JSON, %d messages", len(check2.Messages))
	}

	// Step 4: headroom.Compress with structured messages.
	var hrmessages []headroom.Message
	for _, msg := range s.Messages {
		for _, block := range msg.Content {
			text := ""
			if block.OfText != nil {
				text = block.OfText.Text
			}
			if block.OfToolUse != nil {
				data, _ := json.Marshal(block.OfToolUse)
				text = string(data)
			}
			if block.OfToolResult != nil {
				data, _ := json.Marshal(block.OfToolResult)
				text = string(data)
			}
			if strings.TrimSpace(text) != "" {
				hrmessages = append(hrmessages, headroom.Message{
					Role:    string(msg.Role),
					Content: text,
				})
			}
		}
	}

	opts2 := headroom.DefaultOptions()
	opts2.Aggressiveness = 0.8
	opts2.Reversible = false
	opts2.EnablePipeline = true
	opts2.TokenBudget = 40000

	hrResult, err := headroom.Compress(hrmessages, opts2)
	if err != nil {
		t.Fatalf("headroom.Compress(structured): %v", err)
	}
	t.Logf("headroom.Compress(structured): %d messages -> %d messages, savings=%.1f%%",
		len(hrmessages), len(hrResult.Messages), hrResult.Savings*100)

	// Summarise.
	fmt.Println()
	fmt.Println("=== COMPARISON ===")
	fmt.Printf("Raw JSON                  : %d bytes\n", len(raw))
	fmt.Printf("session.Compact           : %d bytes, %d messages, parseable=%v\n",
		len(compactJSON), len(result.Messages), reflect.TypeOf(check).Kind() != reflect.Invalid)
	fmt.Printf("headroom raw JSON         : %d bytes, parseable=%v\n",
		len(compressed), func() bool { _, e := json.Marshal(check2); return e == nil }())
	fmt.Printf("headroom structured msgs  : %d messages -> %d messages\n",
		len(hrmessages), len(hrResult.Messages))
}
