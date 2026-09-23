// Copyright 2026 Hashir Muzaffar. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license
// that can be found in the LICENSE file.

package hfbpe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// UnsupportedError reports a tokenizer.json this package will not load. It
// names the offending component so the caller can tell at a glance whether
// the file is out of scope or the package is missing a feature.
//
// Load returns this rather than silently ignoring a component, because a
// tokenizer that quietly disagrees with the one a model was trained with is
// worse than one that refuses to load.
type UnsupportedError struct {
	Component string // e.g. "normalizer", "model.type"
	Value     string // the unsupported value found
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("hfbpe: unsupported %s %q", e.Component, e.Value)
}

// Tokenizer is a loaded byte-level BPE tokenizer.
type Tokenizer struct {
	model *bpe

	idToToken map[int]string

	// Added tokens are matched literally against the input before any
	// pre-tokenization, longest first.
	added       []addedToken
	addedByText map[string]addedToken
}

// LoadFile reads a tokenizer.json from disk.
func LoadFile(path string) (*Tokenizer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}

// Load reads a tokenizer.json.
//
// It returns an *UnsupportedError if the file uses any component this
// package does not reproduce exactly: a model other than BPE, any
// normalizer, a pre-tokenizer or decoder other than ByteLevel, a
// post-processor other than ByteLevel, or a BPE option that changes the
// merge algorithm.
func Load(r io.Reader) (*Tokenizer, error) {
	var f tokenizerFile
	dec := json.NewDecoder(r)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("hfbpe: parsing tokenizer.json: %w", err)
	}

	if f.Model.Type != "BPE" {
		return nil, &UnsupportedError{Component: "model.type", Value: f.Model.Type}
	}

	// Pipeline components. Anything not named here is rejected.
	// NFC is a no-op for ASCII and only renorms Unicode; ModernBERT/Laya ship
	// it. TemplateProcessing is ignored because callers that need CLS/SEP
	// (or [MASK] markers) insert them themselves with add_special_tokens=false.
	if t, err := typeOf(f.Normalizer); err != nil {
		return nil, err
	} else if t != "" && t != "NFC" {
		return nil, &UnsupportedError{Component: "normalizer", Value: t}
	}
	for _, c := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"pre_tokenizer", f.PreTokenizer, "ByteLevel"},
		{"decoder", f.Decoder, "ByteLevel"},
	} {
		t, err := typeOf(c.raw)
		if err != nil {
			return nil, err
		}
		// An absent decoder is fine; an absent pre-tokenizer is not, since
		// the byte-level split is what the merge table was trained against.
		if t == "" && c.name != "pre_tokenizer" {
			continue
		}
		if t != c.want {
			return nil, &UnsupportedError{Component: c.name, Value: t}
		}
	}
	if t, err := typeOf(f.PostProc); err != nil {
		return nil, err
	} else if t != "" && t != "ByteLevel" && t != "TemplateProcessing" {
		return nil, &UnsupportedError{Component: "post_processor", Value: t}
	}

	// BPE options that would change the algorithm.
	m := f.Model
	if m.Dropout != nil && *m.Dropout != 0 {
		return nil, &UnsupportedError{Component: "model.dropout", Value: fmt.Sprint(*m.Dropout)}
	}
	if m.ContinuingSubwordPrefix != nil && *m.ContinuingSubwordPrefix != "" {
		return nil, &UnsupportedError{Component: "model.continuing_subword_prefix", Value: *m.ContinuingSubwordPrefix}
	}
	if m.EndOfWordSuffix != nil && *m.EndOfWordSuffix != "" {
		return nil, &UnsupportedError{Component: "model.end_of_word_suffix", Value: *m.EndOfWordSuffix}
	}
	if m.ByteFallback {
		return nil, &UnsupportedError{Component: "model.byte_fallback", Value: "true"}
	}
	if m.FuseUnk {
		return nil, &UnsupportedError{Component: "model.fuse_unk", Value: "true"}
	}

	var vocab map[string]int
	if err := json.Unmarshal(m.Vocab, &vocab); err != nil {
		return nil, fmt.Errorf("hfbpe: parsing model.vocab: %w", err)
	}

	merges, err := parseMerges(m.Merges)
	if err != nil {
		return nil, fmt.Errorf("hfbpe: %w", err)
	}

	ranks := make(map[pair]int, len(merges))
	for i, mp := range merges {
		// Earlier merges win; a duplicate keeps its first rank.
		if _, seen := ranks[pair{mp[0], mp[1]}]; !seen {
			ranks[pair{mp[0], mp[1]}] = i
		}
	}

	unk := -1
	if m.UnkToken != nil && *m.UnkToken != "" {
		id, ok := vocab[*m.UnkToken]
		if !ok {
			return nil, fmt.Errorf("hfbpe: unk_token %q is not in the vocabulary", *m.UnkToken)
		}
		unk = id
	}

	t := &Tokenizer{
		model: &bpe{
			vocab:        vocab,
			ranks:        ranks,
			unkID:        unk,
			ignoreMerges: m.IgnoreMerges,
			cache:        make(map[string][]string),
		},
		idToToken:   make(map[int]string, len(vocab)),
		addedByText: make(map[string]addedToken, len(f.AddedTokens)),
	}
	for tok, id := range vocab {
		t.idToToken[id] = tok
	}
	for _, a := range f.AddedTokens {
		t.added = append(t.added, a)
		t.addedByText[a.Content] = a
		t.idToToken[a.ID] = a.Content
	}
	// Longest first, so that a token that is a prefix of another cannot
	// shadow it.
	sort.SliceStable(t.added, func(i, j int) bool {
		return len(t.added[i].Content) > len(t.added[j].Content)
	})

	return t, nil
}

// VocabSize reports the number of entries in the vocabulary, including added
// tokens.
func (t *Tokenizer) VocabSize() int { return len(t.idToToken) }

// TokenToID returns the id of a token and whether it is in the vocabulary.
func (t *Tokenizer) TokenToID(tok string) (int, bool) {
	if a, ok := t.addedByText[tok]; ok {
		return a.ID, true
	}
	id, ok := t.model.vocab[tok]
	return id, ok
}

// IDToToken returns the token for an id and whether the id is in range.
func (t *Tokenizer) IDToToken(id int) (string, bool) {
	tok, ok := t.idToToken[id]
	return tok, ok
}

// Encoding is the result of tokenizing a string.
type Encoding struct {
	IDs    []int
	Tokens []string
}

// Encode tokenizes s.
func (t *Tokenizer) Encode(s string) Encoding {
	var enc Encoding
	for _, seg := range t.splitAdded(s) {
		if seg.added {
			a := t.addedByText[seg.text]
			enc.IDs = append(enc.IDs, a.ID)
			enc.Tokens = append(enc.Tokens, a.Content)
			continue
		}
		for _, word := range splitGPT2(seg.text) {
			for _, piece := range t.model.tokenize(encodeBytes(word)) {
				id, ok := t.model.vocab[piece]
				if !ok {
					if t.model.unkID < 0 {
						// Cannot happen for a byte-level
						// vocabulary, which covers all 256
						// bytes, but do not silently drop.
						continue
					}
					id = t.model.unkID
					piece = t.idToToken[id]
				}
				enc.IDs = append(enc.IDs, id)
				enc.Tokens = append(enc.Tokens, piece)
			}
		}
	}
	return enc
}

// Decode turns ids back into text. Added tokens are included unless
// skipSpecial is set.
func (t *Tokenizer) Decode(ids []int, skipSpecial bool) string {
	var b strings.Builder
	for _, id := range ids {
		tok, ok := t.idToToken[id]
		if !ok {
			continue
		}
		if a, isAdded := t.addedByText[tok]; isAdded {
			if skipSpecial && a.Special {
				continue
			}
			b.WriteString(tok)
			continue
		}
		b.WriteString(decodeBytes(tok))
	}
	return b.String()
}

type segment struct {
	text  string
	added bool
}

// splitAdded cuts s around any added tokens, which are matched literally and
// never merged with surrounding text.
func (t *Tokenizer) splitAdded(s string) []segment {
	if len(t.added) == 0 {
		return []segment{{text: s}}
	}
	var out []segment
	for i := 0; i < len(s); {
		matched := false
		for _, a := range t.added {
			if a.Content != "" && strings.HasPrefix(s[i:], a.Content) {
				out = append(out, segment{text: a.Content, added: true})
				i += len(a.Content)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		// Accumulate plain text until the next added token starts.
		j := i
		for j < len(s) {
			hit := false
			for _, a := range t.added {
				if a.Content != "" && strings.HasPrefix(s[j:], a.Content) {
					hit = true
					break
				}
			}
			if hit {
				break
			}
			j++
		}
		out = append(out, segment{text: s[i:j]})
		i = j
	}
	return out
}
