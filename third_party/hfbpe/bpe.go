// Copyright 2026 Hashir Muzaffar. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license
// that can be found in the LICENSE file.

package hfbpe

import "sync"

// pair is an adjacent symbol pair considered for merging.
type pair struct{ a, b string }

// bpe holds the merge table and vocabulary of a byte pair encoding model.
type bpe struct {
	vocab        map[string]int
	ranks        map[pair]int
	unkID        int // -1 when the model has no unknown token
	ignoreMerges bool

	mu    sync.RWMutex
	cache map[string][]string
}

// maxCacheEntries bounds the word cache so that adversarial input cannot
// grow it without limit. Natural text saturates well below this.
const maxCacheEntries = 1 << 16

// tokenize applies the merge table to a single pre-token, returning the
// resulting vocabulary pieces.
func (m *bpe) tokenize(word string) []string {
	if m.ignoreMerges {
		if _, ok := m.vocab[word]; ok {
			return []string{word}
		}
	}

	m.mu.RLock()
	cached, ok := m.cache[word]
	m.mu.RUnlock()
	if ok {
		return cached
	}

	syms := splitRunes(word)
	if len(syms) > 1 {
		syms = m.merge(syms)
	}

	m.mu.Lock()
	if len(m.cache) < maxCacheEntries {
		m.cache[word] = syms
	}
	m.mu.Unlock()
	return syms
}

// merge repeatedly applies the lowest ranked available merge until none of
// the adjacent pairs appears in the table. Every occurrence of the chosen
// pair is merged before the ranks are consulted again, which is what the
// reference implementation does.
func (m *bpe) merge(syms []string) []string {
	for {
		best, bestRank, found := pair{}, 0, false
		for i := 0; i+1 < len(syms); i++ {
			p := pair{syms[i], syms[i+1]}
			r, ok := m.ranks[p]
			if !ok {
				continue
			}
			if !found || r < bestRank {
				best, bestRank, found = p, r, true
			}
		}
		if !found {
			return syms
		}

		out := syms[:0:0]
		for i := 0; i < len(syms); {
			if i+1 < len(syms) && syms[i] == best.a && syms[i+1] == best.b {
				out = append(out, best.a+best.b)
				i += 2
				continue
			}
			out = append(out, syms[i])
			i++
		}
		syms = out
	}
}

// splitRunes splits s into one string per rune.
func splitRunes(s string) []string {
	out := make([]string, 0, len(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}
