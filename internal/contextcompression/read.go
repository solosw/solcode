package contextcompression

import "fmt"

// readSource paginates a single serialized message on rune boundaries, including
// large tool outputs/images that cannot fit in one tool result.
func (s *State) readSource(ref string, offset, limit int) (map[string]any, error) {
	m, err := s.message(ref)
	if err != nil {
		return nil, err
	}
	text := []rune(Text(m))
	if offset < 0 || offset > len(text) {
		return nil, fmt.Errorf("invalid character offset")
	}
	if limit <= 0 {
		limit = 4000
	}
	if limit > 8000 {
		limit = 8000
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	return map[string]any{"ref": ref, "text": string(text[offset:end]), "offset": offset, "next_offset": end, "total_runes": len(text)}, nil
}
