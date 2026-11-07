package contextcompression

import sdk "github.com/anthropics/anthropic-sdk-go"

// Handoff archives an explicit /compact replacement. Its legacy wire format
// remains unchanged while the replaced source stays reachable via decompress.
func (s *State) Handoff(before, after []sdk.MessageParam, summary string) error {
	if err := s.Sync(before); err != nil {
		return err
	}
	if len(after) == 0 {
		return nil
	}
	block := Block{Ref: s.nextBlockRef(), Topic: "Explicit session handoff", Summary: summary, Children: append([]string(nil), s.Active...), Level: 1, View: &after[0]}
	for _, ref := range s.Active {
		if b := s.block(ref); b != nil && b.Level >= block.Level {
			block.Level = b.Level + 1
		}
	}
	s.Blocks = append(s.Blocks, block)
	s.Active = []string{block.Ref}
	s.Revision++
	return s.Sync(after)
}
