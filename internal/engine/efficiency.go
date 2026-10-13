package engine

import (
	"crypto/sha256"
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
	started   bool

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
	if c.started && !c.newThisTurn {
		c.idleTurns++
	} else if c.started {
		c.idleTurns = 0
	}
	c.turn = turn
	c.started = true
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

	c.observeCallLocked(name, input, result)
	c.trimLocked()
}

func (c *RequestEfficiencyController) observeCallLocked(name string, input []byte, result ToolResult) {
	switch name {
	case tool.ViewToolName, "LSP":
		c.recordReadLocked(readSignature(name, input), result.IsError)
	case tool.GrepToolName, tool.GlobToolName:
		if result.IsError {
			break
		}
		sig := searchSignature(name, input)
		if sig != "" {
			c.searches[sig]++
			if c.searches[sig] == 1 {
				c.newThisTurn = true
			}
		}
	case tool.EditToolName, tool.WriteToolName, tool.MultiEditToolName, tool.MultiWriteToolName, tool.PatchToolName:
		if !result.IsError {
			// A new file version makes previous reads/searches legitimate again.
			c.newThisTurn = true
			for _, path := range tool.PathsForCheckpoint(name, input) {
				for sig := range c.reads {
					if strings.HasPrefix(sig, strings.TrimSpace(path)+" [") {
						delete(c.reads, sig)
					}
				}
			}
			c.searches = map[string]int{}
		}
	case tool.BashToolName:
		if !result.IsError {
			c.newThisTurn = true
			// Shell commands may mutate files outside the edit tools.
			c.reads = map[string]int{}
			c.searches = map[string]int{}
		}
	case tool.BatchToolName:
		c.observeBatchLocked(input, result)
	}

	if result.IsError {
		if sig := errorSignature(result); sig != "" {
			c.errors[sig]++
		}
	}
}

func (c *RequestEfficiencyController) observeBatchLocked(input []byte, result ToolResult) {
	if result.IsError || result.Content == nil {
		return
	}
	var resp struct {
		Results []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Reverted bool   `json:"reverted"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(result.Content.Text), &resp) != nil {
		return
	}
	operations := batchObservedOperations(input)
	byID := make(map[string]batchObservedOperation, len(operations))
	for _, op := range operations {
		byID[op.ID] = op
	}
	for _, child := range resp.Results {
		op, exists := byID[child.ID]
		if !exists || child.Status == "skipped" || child.Reverted {
			continue
		}
		childResult := ToolResult{IsError: child.Status != "success"}
		if child.Error != nil {
			childResult.Content = tool.ErrorResult(child.Error.Message)
		}
		c.observeCallLocked(op.Tool, op.Input, childResult)
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
		for _, path := range boundedEfficiencyItems(repeated) {
			items = append(items, fmt.Sprintf("%s (read %d×)", oneLine(path, 200), c.reads[path]))
		}
		parts = append(parts, "Repeated identical read window — reuse the earlier retained result if current and sufficient; changed files, truncated output, or new ranges may need another read: "+strings.Join(items, ", "))
	}
	if repeated := c.repeatedSearchesLocked(); len(repeated) > 0 {
		items := make([]string, 0, len(repeated))
		for _, sig := range boundedEfficiencyItems(repeated) {
			items = append(items, fmt.Sprintf("%s (%d×)", sig, c.searches[sig]))
		}
		parts = append(parts, "Repeated identical search — reuse the earlier retained result unless files or scope changed: "+strings.Join(items, ", "))
	}
	if repeated := c.repeatedErrorsLocked(); len(repeated) > 0 {
		items := make([]string, 0, len(repeated))
		for _, sig := range boundedEfficiencyItems(repeated) {
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

// Summaries are bounded independently from the local tracking maps.
func boundedEfficiencyItems(items []string) []string {
	const maxItems = 8
	if len(items) > maxItems {
		return items[:maxItems]
	}
	return items
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

// readSignature distinguishes pagination and LSP queries. Two different
// windows of the same file are new information, not a redundant file read.
func readSignature(name string, input []byte) string {
	var params map[string]any
	if json.Unmarshal(input, &params) != nil {
		return ""
	}
	path, _ := params["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if name == tool.ViewToolName {
		if offset, _ := params["offset"].(float64); offset == 0 {
			delete(params, "offset")
		}
		if limit, _ := params["limit"].(float64); limit == 0 || limit == float64(tool.DefaultReadLimit) {
			delete(params, "limit")
		}
	}
	return path + " [" + name + ":" + observationSignature(params) + "]"
}

// Include all search options, not just term/path: changed case, output mode,
// glob or pagination may be a necessary follow-up search.
func searchSignature(name string, input []byte) string {
	var params map[string]any
	if json.Unmarshal(input, &params) != nil {
		return ""
	}
	term, _ := params["pattern"].(string)
	if term == "" {
		term, _ = params["query"].(string)
	}
	if strings.TrimSpace(term) == "" {
		return ""
	}
	path, _ := params["path"].(string)
	return name + ":" + oneLine(term, 80) + "@" + oneLine(path, 80) + " [" + observationSignature(params) + "]"
}

func observationSignature(params map[string]any) string {
	data, _ := json.Marshal(params)
	return fmt.Sprintf("%x", sha256.Sum256(data))[:12]
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
