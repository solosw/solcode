package contextcompression

import (
	"fmt"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

// Fork retains only evidence reachable from the selected inheritance window.
// The child's active view and future folds never mutate its parent's state.
func (s *State) Fork(messages []sdk.MessageParam) *State {
	next := s.Clone()
	if err := next.Sync(messages); err != nil {
		return next
	}
	keep := map[string]bool{}
	var visit func(string)
	visit = func(ref string) {
		if keep[ref] {
			return
		}
		keep[ref] = true
		if b := next.block(ref); b != nil {
			for _, child := range b.Children {
				visit(child)
			}
		}
	}
	for _, ref := range next.Active {
		visit(ref)
	}
	sources := next.Sources[:0]
	for _, entry := range next.Sources {
		if keep[entry.Ref] {
			sources = append(sources, entry)
		}
	}
	next.Sources = sources
	next.sourceIndex = nil
	blocks := next.Blocks[:0]
	for _, block := range next.Blocks {
		if keep[block.Ref] {
			blocks = append(blocks, block)
		}
	}
	next.Blocks = blocks
	next.blockIndex = nil
	return next
}
func (s *State) nextSourceRef() string {
	if s.NextMessage > 0 {
		s.NextMessage++
		return fmt.Sprintf("m%d", s.NextMessage)
	}
	max := 0
	for _, entry := range s.Sources {
		var n int
		_, _ = fmt.Sscanf(entry.Ref, "m%d", &n)
		if n > max {
			max = n
		}
	}
	s.NextMessage = max + 1
	return fmt.Sprintf("m%d", s.NextMessage)
}
func (s *State) nextBlockRef() string {
	if s.NextBlock > 0 {
		s.NextBlock++
		return fmt.Sprintf("b%d", s.NextBlock)
	}
	max := 0
	for _, block := range s.Blocks {
		var n int
		_, _ = fmt.Sscanf(block.Ref, "b%d", &n)
		if n > max {
			max = n
		}
	}
	s.NextBlock = max + 1
	return fmt.Sprintf("b%d", s.NextBlock)
}
