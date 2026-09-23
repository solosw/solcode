// Copyright 2026 Hashir Muzaffar. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license
// that can be found in the LICENSE file.

package hfbpe

import (
	"encoding/json"
	"fmt"
	"strings"
)

// tokenizerFile mirrors the subset of tokenizer.json this package reads.
// Components that are parsed but unsupported are still decoded so that Load
// can name them in its error.
type tokenizerFile struct {
	Version      string          `json:"version"`
	AddedTokens  []addedToken    `json:"added_tokens"`
	Normalizer   json.RawMessage `json:"normalizer"`
	PreTokenizer json.RawMessage `json:"pre_tokenizer"`
	PostProc     json.RawMessage `json:"post_processor"`
	Decoder      json.RawMessage `json:"decoder"`
	Model        modelJSON       `json:"model"`
}

type addedToken struct {
	ID         int    `json:"id"`
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	LStrip     bool   `json:"lstrip"`
	RStrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
	Special    bool   `json:"special"`
}

type modelJSON struct {
	Type                    string   `json:"type"`
	Dropout                 *float64 `json:"dropout"`
	UnkToken                *string  `json:"unk_token"`
	ContinuingSubwordPrefix *string  `json:"continuing_subword_prefix"`
	EndOfWordSuffix         *string  `json:"end_of_word_suffix"`
	FuseUnk                 bool     `json:"fuse_unk"`
	ByteFallback            bool     `json:"byte_fallback"`
	IgnoreMerges            bool     `json:"ignore_merges"`
	// Vocab is left raw so that model.type can be checked before it is
	// decoded: Unigram stores vocab as an array of [token, score] pairs,
	// which would fail to unmarshal into a map and mask the real reason
	// the file is unsupported.
	Vocab  json.RawMessage `json:"vocab"`
	Merges json.RawMessage `json:"merges"`
}

// component is the shared shape of the pipeline stages: every one of them
// carries a "type" discriminator.
type component struct {
	Type string `json:"type"`
}

// typeOf returns the "type" field of a pipeline component, or "" if the
// component is JSON null or absent.
func typeOf(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var c component
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	return c.Type, nil
}

// parseMerges accepts both encodings of the merge table: the modern
// [["a","b"], ...] form and the older ["a b", ...] form.
func parseMerges(raw json.RawMessage) ([][2]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var pairs [][2]string
	if err := json.Unmarshal(raw, &pairs); err == nil {
		return pairs, nil
	}

	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, fmt.Errorf("merges: not a list of pairs or of strings: %w", err)
	}
	out := make([][2]string, 0, len(lines))
	for i, ln := range lines {
		// Upstream splits on ASCII space and requires exactly two pieces,
		// so a piece may never itself contain a space. Reject anything
		// else rather than guessing at a split point.
		sp := strings.IndexByte(ln, ' ')
		if sp <= 0 || sp == len(ln)-1 || strings.IndexByte(ln[sp+1:], ' ') >= 0 {
			return nil, fmt.Errorf("merges[%d]: %q is not exactly two space-separated pieces", i, ln)
		}
		out = append(out, [2]string{ln[:sp], ln[sp+1:]})
	}
	return out, nil
}
