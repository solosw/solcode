package contextcompression

// ContextRange endpoints are inclusive visible references. Compressible ranges
// contain whole tool exchanges and never intersect a protected message.
type ContextRange struct {
	Start            string `json:"start"`
	End              string `json:"end"`
	Messages         int    `json:"messages"`
	Tokens           int    `json:"tokens"`
	EstimatedSavings int    `json:"estimated_savings_tokens,omitempty"`
}

type Overview struct {
	Revision                int              `json:"revision"`
	ActiveTokens            int              `json:"active_tokens"`
	MaxContextTokens        int              `json:"max_context_tokens"`
	CompressibleTokens      int              `json:"compressible_tokens"`
	EstimatedSavings        int              `json:"estimated_savings_tokens"`
	CompressibleRanges      []ContextRange   `json:"compressible_ranges"`
	ProtectedRanges         []ContextRange   `json:"protected_ranges"`
	Blocks                  []map[string]any `json:"blocks"`
	TotalCompressibleRanges int              `json:"total_compressible_ranges"`
	TotalProtectedRanges    int              `json:"total_protected_ranges"`
}

// Overview describes the entire view even when the detailed status is paginated.
// limit bounds each list, not the totals; zero returns all ranges.
func (s *State) Overview(maxTokens, limit int) Overview {
	messages, _ := s.Messages()
	out := Overview{Revision: s.Revision, ActiveTokens: Tokens(messages), MaxContextTokens: maxTokens,
		CompressibleRanges: []ContextRange{}, ProtectedRanges: []ContextRange{}, Blocks: s.BlocksMetadata()}
	safe, keep := boundaries(messages), protectedIndexes(messages)
	// Expand protection to the enclosing exchange. Otherwise a suggested range
	// could start inside a tool group containing a protected overlay or request.
	blocked := make([]bool, len(messages))
	for a := 0; a < len(messages); {
		b := a + 1
		for b < len(messages) && !safe[b] {
			b++
		}
		protect := !safe[a] || !safe[b]
		for i := a; i < b; i++ {
			protect = protect || keep[i]
		}
		for i := a; i < b; i++ {
			blocked[i] = protect
		}
		a = b
	}
	for a := 0; a < len(messages); {
		b := a + 1
		for b < len(messages) && blocked[b] == blocked[a] {
			b++
		}
		r := ContextRange{Start: s.Active[a], End: s.Active[b-1], Messages: b - a, Tokens: Tokens(messages[a:b])}
		if blocked[a] {
			out.ProtectedRanges = append(out.ProtectedRanges, r)
		} else {
			// A planning estimate only: retain one quarter plus the fold wrapper.
			// Compress still checks the actual summary's token reduction.
			r.EstimatedSavings = max(0, r.Tokens*3/4-128)
			out.CompressibleRanges = append(out.CompressibleRanges, r)
			out.CompressibleTokens += r.Tokens
			out.EstimatedSavings += r.EstimatedSavings
		}
		a = b
	}
	out.TotalCompressibleRanges = len(out.CompressibleRanges)
	out.TotalProtectedRanges = len(out.ProtectedRanges)
	if limit > 0 {
		out.CompressibleRanges = out.CompressibleRanges[:min(limit, len(out.CompressibleRanges))]
		out.ProtectedRanges = out.ProtectedRanges[:min(limit, len(out.ProtectedRanges))]
		out.Blocks = out.Blocks[max(0, len(out.Blocks)-limit):]
	}
	return out
}
