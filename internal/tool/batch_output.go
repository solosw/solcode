package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// compactOutputs bounds combined child text (not JSON metadata), preserving a
// recoverable full result before trimming. Artifacts are private temporary
// files, not repository changes, and are read back with View offset/limit.
func (bt *batchTool) compactOutputs(ctx context.Context, uctx *UseContext, response *batchResponse, mode string) {
	type textField struct {
		index   int
		full    string
		text    *string
		output  *batchOutput
		failure *batchError
	}
	fields := make([]textField, 0, len(response.Results))
	for i := range response.Results {
		r := &response.Results[i]
		if r.Output != nil {
			full := r.Output.Content
			if mode != "full" && !r.Reverted {
				switch r.Tool {
				case EditToolName, WriteToolName, PatchToolName:
					// These tools repeat the full diff. Their first two lines describe the
					// target and change counts; MultiEdit/MultiWrite already emit summaries.
					body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(full, "<result>\n"), "</result>"))
					lines := strings.SplitN(body, "\n", 3)
					if len(lines) > 2 {
						r.Output.Content = strings.Join(lines[:2], "\n")
						r.Output.Summarized = true
					}
				}
			}
			fields = append(fields, textField{index: i, full: full, text: &r.Output.Content, output: r.Output})
		} else if r.Error != nil {
			fields = append(fields, textField{index: i, full: r.Error.Message, text: &r.Error.Message, failure: r.Error})
		}
	}
	// Short fields consume only what they need. Max-min fair allocation avoids
	// losing all later outputs behind one huge View result.
	allocations := make([]int, len(fields))
	remaining := bt.limits.MaxTotalResultRunes
	pending := make([]int, len(fields))
	for i := range pending {
		pending[i] = i
	}
	for len(pending) > 0 {
		share := remaining / len(pending)
		next := make([]int, 0, len(pending))
		for _, i := range pending {
			need := min(len([]rune(*fields[i].text)), bt.limits.MaxResultRunes)
			if need <= share {
				allocations[i] = need
				remaining -= need
			} else {
				next = append(next, i)
			}
		}
		if len(next) == len(pending) {
			for _, i := range next {
				allocations[i] = remaining / len(next)
			}
			break
		}
		pending = next
	}
	dir := ""
	for i, field := range fields {
		runes := []rune(*field.text)
		truncated := len(runes) > allocations[i]
		if truncated {
			*field.text = truncateBatchText(runes, allocations[i])
		}
		if field.output != nil {
			field.output.Truncated = truncated
		}
		if field.failure != nil {
			field.failure.Truncated = truncated
		}
		if field.full == *field.text {
			continue
		}
		var err error
		if dir == "" {
			dir, err = os.MkdirTemp("", "solcode-batch-outputs-")
		}
		path := filepath.Join(dir, fmt.Sprintf("%s_%d.txt", response.BatchID, field.index+1))
		if err == nil {
			// ACP View reads client-owned files, so archive through that filesystem
			// only if both read and write capabilities are available.
			if uctx != nil && uctx.TextFileSystem != nil && uctx.TextFileSystem.CanReadTextFile() {
				if !uctx.TextFileSystem.CanWriteTextFile() {
					err = fmt.Errorf("client filesystem cannot archive output: write capability unavailable")
				} else {
					err = uctx.TextFileSystem.WriteTextFile(ctx, path, field.full)
				}
			} else {
				err = os.WriteFile(path, []byte(field.full), 0o600)
			}
		}
		if err != nil {
			response.OutputStorageError = "Full output unavailable: " + err.Error()
			continue
		}
		if field.output != nil {
			field.output.FullOutputPath = path
		}
		if field.failure != nil {
			field.failure.FullOutputPath = path
		}
	}
}

// Preserve the end of long diagnostics (exit status, summary, stack trace), not
// just the beginning. The marker itself is included in the rune budget.
func truncateBatchText(runes []rune, budget int) string {
	if len(runes) <= budget {
		return string(runes)
	}
	marker := []rune("\n…truncated…\n")
	if budget <= len(marker)+1 {
		return string(runes[:budget])
	}
	remaining := budget - len(marker)
	head := (remaining + 1) / 2
	tail := remaining - head
	return string(runes[:head]) + string(marker) + string(runes[len(runes)-tail:])
}
