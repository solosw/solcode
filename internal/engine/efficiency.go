package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/solosw/solcode/internal/tool"
)

// RequestEfficiencyController tracks cheap, local signals that indicate the
// agent is looping instead of making progress (§7):
//
//   - the same file read repeatedly
//   - the same search repeated
//   - the same error recurring
//   - consecutive turns with no new information
//
// It only annotates context with known results and next-step hints. It never
// issues model requests, never executes operations, and never terminates a run
// on a request budget.
type RequestEfficiencyController struct {
	mu sync.Mutex

	// reads counts how many times each path was read.
	reads map[string]int
	// searches counts normalized search signatures.
	searches map[string]int
	// errors counts normalized error signatures.
	errors map[string]int
	// newThisTurn marks whether the current turn produced fresh information.
	newThisTurn bool
	// idleTurns counts consecutive turns with no new information.
	idleTurns int
	turn      int

	// bounds so the controller itself cannot grow without limit.
	maxTracked int
}

const (
	defaultEfficiencyMaxTracked = 128
	// idleTurnHintThreshold is how many consecutive no-progress turns before the
	// controller surfaces a hint. It is advisory only.
	idleTurnHintThreshold = 2
)

// NewRequestEfficiencyController builds an empty controller.
func NewRequestEfficiencyController() *RequestEfficiencyController {
	return &RequestEfficiencyController{
		reads:      map[string]int{},
		searches:   map[string]int{},
		errors:     map[string]int{},
		maxTracked: defaultEfficiencyMaxTracked,
	}
}

// BeginTurn advances the turn counter and rolls the progress window: a turn
// that added no new information increments idleTurns.
func (c *RequestEfficiencyController) BeginTurn(turn int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turn != 0 && !c.newThisTurn {
		c.idleTurns++
	} else if c.turn != 0 {
		c.idleTurns = 0
	}
	c.turn = turn
	c.newThisTurn = false
}

// ObserveCall records one tool call: which files were read and which searches
// ran. Reads/searches seen before are marked as repeats (not new information).
func (c *RequestEfficiencyController) ObserveCall(name string, input []byte, result ToolResult) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	switch name {
	case tool.ViewToolName, "LSP":
		for _, path := range tool.PathsForCheckpoint(tool.EditToolName, input) {
			c.recordReadLocked(path, result.IsError)
		}
	case tool.GrepToolName, tool.GlobToolName:
		sig := searchSignature(name, input)
		if sig != "" {
			c.searches[sig]++
			c.newThisTurn = true
		}
	case tool.EditToolName, tool.WriteToolName, tool.MultiEditToolName, tool.MultiWriteToolName, tool.PatchToolName:
		if !result.IsError {
			// A successful mutation is always progress.
			c.newThisTurn = true
		}
	case tool.BashToolName:
		if !result.IsError {
			c.newThisTurn = true
		}
	case tool.BatchToolName:
		c.observeBatchLocked(input, result)
	}

	if result.IsError {
		if sig := errorSignature(result); sig != "" {
			c.errors[sig]++
		}
	}
	c.trimLocked()
}

func (c *RequestEfficiencyController) observeBatchLocked(input []byte, result ToolResult) {
	var req struct {
		Operations []struct {
			ID    string          `json:"id"`
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"operations"`
	}
	if json.Unmarshal(input, &req) != nil {
		return
	}
	success := map[string]bool{}
	if !result.IsError && result.Content != nil {
		var resp struct {
			Results []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"results"`
		}
		if json.Unmarshal([]byte(result.Content.Text), &resp) == nil {
			for _, r := range resp.Results {
				success[r.ID] = r.Status == "success"
			}
		}
	}
	for _, op := range req.Operations {
		failed := !success[op.ID]
		switch op.Tool {
		case tool.ViewToolName, "LSP":
			for _, path := range tool.PathsForCheckpoint(tool.EditToolName, op.Input) {
				c.recordReadLocked(path, failed)
			}
		case tool.EditToolName, tool.WriteToolName, tool.MultiEditToolName, tool.MultiWriteToolName, tool.PatchToolName:
			if !failed {
				c.newThisTurn = true
			}
		}
	}
}

func (c *RequestEfficiencyController) recordReadLocked(path string, failed bool) {
	path = strings.TrimSpace(path)
	if path == "" || failed {
		return
	}
	c.reads[path]++
	if c.reads[path] == 1 {
		c.newThisTurn = true
	}
}

// Hint returns an advisory note for the model when repeats or idle turns are
// detected. Empty means the controller has nothing useful to add. It only
// reports known results and suggests next steps; it never mandates a tool.
func (c *RequestEfficiencyController) Hint() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var parts []string
	if repeated := c.repeatedReadsLocked(); len(repeated) > 0 {
		items := make([]string, 0, len(repeated))
		for _, path := range repeated {
			items = append(items, fmt.Sprintf("%s (read %d×)", path, c.reads[path]))
		}
		parts = append(parts, "Already read this run — reuse the earlier result instead of reading again: "+strings.Join(items, ", "))
	}
	if repeated := c.repeatedSearchesLocked(); len(repeated) > 0 {
		items := make([]string, 0, len(repeated))
		for _, sig := range repeated {
			items = append(items, fmt.Sprintf("%s (%d×)", sig, c.searches[sig]))
		}
		parts = append(parts, "Repeated search — results are unchanged: "+strings.Join(items, ", "))
	}
	if repeated := c.repeatedErrorsLocked(); len(repeated) > 0 {
		items := make([]string, 0, len(repeated))
		for _, sig := range repeated {
			items = append(items, fmt.Sprintf("%s (%d×)", sig, c.errors[sig]))
		}
		parts = append(parts, "Error already seen — change the approach rather than retrying as-is: "+strings.Join(items, ", "))
	}
	if c.idleTurns >= idleTurnHintThreshold {
		parts = append(parts, fmt.Sprintf("%d consecutive turns without new file reads, edits, or search results. State the blocker or take a different next step.", c.idleTurns))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Efficiency note (local, advisory):\n- " + strings.Join(parts, "\n- ")
}

func (c *RequestEfficiencyController) repeatedReadsLocked() []string {
	var out []string
	for path, count := range c.reads {
		if count >= 2 {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func (c *RequestEfficiencyController) repeatedSearchesLocked() []string {
	var out []string
	for sig, count := range c.searches {
		if count >= 2 {
			out = append(out, sig)
		}
	}
	sort.Strings(out)
	return out
}

func (c *RequestEfficiencyController) repeatedErrorsLocked() []string {
	var out []string
	for sig, count := range c.errors {
		if count >= 2 {
			out = append(out, sig)
		}
	}
	sort.Strings(out)
	return out
}

func (c *RequestEfficiencyController) trimLocked() {
	if len(c.errors) > c.maxTracked {
		c.errors = map[string]int{}
	}
	if len(c.reads) > c.maxTracked {
		c.reads = map[string]int{}
	}
	if len(c.searches) > c.maxTracked {
		c.searches = map[string]int{}
	}
}

// searchSignature normalizes a Grep/Glob call into a stable signature so the
// same query is recognized regardless of parameter order.
func searchSignature(name string, input []byte) string {
	var params struct {
		Pattern string `json:"pattern"`
		Query   string `json:"query"`
		Path    string `json:"path"`
	}
	if json.Unmarshal(input, &params) != nil {
		return ""
	}
	term := strings.TrimSpace(params.Pattern)
	if term == "" {
		term = strings.TrimSpace(params.Query)
	}
	if term == "" {
		return ""
	}
	return name + ":" + term + "@" + strings.TrimSpace(params.Path)
}

// errorSignature reduces an error result to a short, stable signature.
func errorSignature(result ToolResult) string {
	if result.Content == nil {
		return ""
	}
	text := strings.TrimSpace(result.Content.Text)
	if text == "" {
		return ""
	}
	return oneLine(text, 120)
}
