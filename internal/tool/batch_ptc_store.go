package tool

import (
	"fmt"
	"unicode/utf8"
)

const ptcMaxStoreBytes = 8 << 20

// PTCResultStore is invocation-scoped and only touched by the VM owner. Opaque
// references cannot read files, other sessions, or prior invocations.
type ptcResultStore struct {
	prefix       string
	entries      map[string]*ContentBlock
	bytes        int
	previewRunes int
}

func newPTCResultStore(previewRunes int) *ptcResultStore {
	return &ptcResultStore{prefix: nextBatchID(), entries: make(map[string]*ContentBlock), previewRunes: previewRunes}
}

func (s *ptcResultStore) put(result *ContentBlock) map[string]any {
	ref := fmt.Sprintf("%s_result_%d", s.prefix, len(s.entries)+1)
	if len(result.Text) > ptcMaxStoreBytes-s.bytes {
		// Do not turn a successful mutation into a failed operation merely
		// because its full receipt cannot be retained.
		response := s.page(result, "", 0, s.previewRunes)
		response["storage_error"] = "RESULT_STORE_LIMIT: full result was not retained"
		return response
	}
	copy := *result
	s.entries[ref] = &copy
	s.bytes += len(result.Text)
	return s.page(&copy, ref, 0, s.previewRunes)
}

func (s *ptcResultStore) get(ref string, offset, limit int) map[string]any {
	result, ok := s.entries[ref]
	if !ok {
		return ptcError("RESULT_NOT_FOUND")
	}
	if offset < 0 || limit < 0 || offset > utf8.RuneCountInString(result.Text) {
		return ptcError("INVALID_INPUT: offset or limit out of range")
	}
	if limit == 0 || limit > s.previewRunes {
		limit = s.previewRunes
	}
	return s.page(result, ref, offset, limit)
}

func (s *ptcResultStore) page(result *ContentBlock, ref string, offset, limit int) map[string]any {
	text := []rune(result.Text)
	end := offset + min(limit, len(text)-offset)
	response := map[string]any{
		"ok": !result.IsError, "content": string(text[offset:end]),
		"offset": offset, "next_offset": end, "total_runes": len(text), "truncated": end < len(text),
	}
	if ref != "" {
		response["result_ref"] = ref
	}
	return response
}
