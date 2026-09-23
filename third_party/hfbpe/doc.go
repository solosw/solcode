// Copyright 2026 Hashir Muzaffar. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license
// that can be found in the LICENSE file.

// Package hfbpe loads a Hugging Face tokenizer.json and reproduces its
// encoding, for byte-level BPE tokenizers only.
//
// It covers one algorithm deliberately. A tokenizer that silently disagrees
// with the one a model was trained against is worse than no tokenizer at
// all, because the disagreement shows up as quietly degraded output rather
// than as an error. So Load refuses any file it cannot reproduce exactly and
// names the component at fault:
//
//	tok, err := hfbpe.LoadFile("tokenizer.json")
//	var ue *hfbpe.UnsupportedError
//	if errors.As(err, &ue) {
//		// e.g. hfbpe: unsupported model.type "WordPiece"
//	}
//
// # Supported
//
//   - model.type "BPE", with the merge table in either the modern
//     [["a","b"], ...] form or the older ["a b", ...] form
//   - a ByteLevel pre-tokenizer, post-processor and decoder
//   - added and special tokens, matched literally, longest first
//   - model.ignore_merges
//
// # Not supported
//
// WordPiece and Unigram are out of scope, as are all normalizers, any
// pre-tokenizer other than ByteLevel, and the BPE options that change the
// merge algorithm: dropout, byte_fallback, fuse_unk,
// continuing_subword_prefix and end_of_word_suffix. Each is rejected by name
// rather than ignored. Token offsets, truncation and padding are also out of
// scope.
//
// # Verification
//
// The test suite checks token ids, token strings and decoded text against
// the Hugging Face tokenizers library over a corpus that exercises
// whitespace runs, contractions, multi-script text, emoji and ZWJ sequences,
// combining marks, invisible characters and embedded special tokens. The
// fixtures are regenerable; see cmd/genfixtures.
package hfbpe
