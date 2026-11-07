package app

import (
	"strings"
	"testing"
)

func testSessionHandoff() string {
	return strings.Join([]string{
		"## Active Request and Intent", "- 修复会话压缩，保留用户原始约束。",
		"## Constraints and Decisions", "- Do not change the summary-only retention policy.",
		"## Progress and Current State", "- done: implemented summary generation; pending: verification.",
		"## Files and Symbols", "- `internal/app/app.go`: `compactSession` preserves the generated handoff.",
		"## Errors and Verification", "- `go test ./internal/app` failed; exit code 1.",
		"```text", "## Next Step", "return status = failed", "```",
		"## Open Tasks and Blockers", "- None",
		"## Current Work", "- None",
		"## Next Step", "- Re-run targeted tests; result not yet verified.",
	}, "\n")
}

func TestHandoffSummarySanitizationPreservesStructure(t *testing.T) {
	summary := testSessionHandoff()
	for _, input := range []string{summary, summary + "\n(File has 1038 more lines. Use 'offset' parameter to read beyond line 260)"} {
		got := sanitizeLoadedSessionSummary(input)
		if got != summary {
			t.Fatalf("handoff changed during cleanup:\n%s", got)
		}
		if again := sanitizeLoadedSessionSummary(got); again != got {
			t.Fatal("handoff cleanup is not idempotent")
		}
	}
}
