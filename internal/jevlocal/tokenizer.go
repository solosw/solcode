package jevlocal

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	sp "github.com/tggo/goSentencePiece"
)

// OpenJev special-token IDs from the shipped tokenizer_config / added_tokens.
const (
	openJevPadID   = 0
	openJevCLSID   = 1
	openJevSEPID   = 2
	openJevUnkID   = 3
	openJevMaskID  = 128000
	openJevStateID = 128001
	openJevQID     = 128002
	openJevOptID   = 128003
)

// openJevTokenizer wraps a SentencePiece / HF tokenizer for OpenJev markers.
type openJevTokenizer struct {
	tok     *sp.Tokenizer
	clsID   int
	sepID   int
	stateID int
	qID     int
	optID   int
}

func loadOpenJevTokenizer(modelDir string) (*openJevTokenizer, error) {
	jsonPath := filepath.Join(modelDir, "tokenizer.json")
	spmPath := filepath.Join(modelDir, "spm.model")

	var (
		tok *sp.Tokenizer
		err error
	)
	// Prefer spm.model: OpenJev markers live outside the base 128k SPM vocab
	// (IDs 128001+) and HuggingFace tokenizer.json loading currently drops
	// those added tokens / can insert spurious UNKs for DebertaV2 exports.
	if fileExists(spmPath) {
		tok, err = sp.NewTokenizer(spmPath)
		if err != nil {
			return nil, fmt.Errorf("load spm.model: %w", err)
		}
	} else if fileExists(jsonPath) {
		tok, err = sp.NewTokenizerFromJSON(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("load tokenizer.json: %w", err)
		}
	} else {
		return nil, fmt.Errorf("open-jev tokenizer: need spm.model or tokenizer.json in %s", modelDir)
	}

	ot := &openJevTokenizer{
		tok:     tok,
		clsID:   openJevCLSID,
		sepID:   openJevSEPID,
		stateID: openJevStateID,
		qID:     openJevQID,
		optID:   openJevOptID,
	}
	// CLS/SEP live in the base SPM vocab; refine from the model when present.
	if id := tok.Model().PieceToId("[CLS]"); tok.Model().IdToPiece(id) == "[CLS]" {
		ot.clsID = id
	}
	if id := tok.Model().PieceToId("[SEP]"); tok.Model().IdToPiece(id) == "[SEP]" {
		ot.sepID = id
	}
	// STATE/Q/OPT/MASK are OpenJev additions beyond vocab_size=128000 — keep
	// the shipped IDs even when PieceToId falls back to UNK.
	if id := tok.Model().PieceToId("[STATE]"); tok.Model().IdToPiece(id) == "[STATE]" {
		ot.stateID = id
	}
	if id := tok.Model().PieceToId("[Q]"); tok.Model().IdToPiece(id) == "[Q]" {
		ot.qID = id
	}
	if id := tok.Model().PieceToId("[OPT]"); tok.Model().IdToPiece(id) == "[OPT]" {
		ot.optID = id
	}
	_ = openJevPadID
	_ = openJevUnkID
	_ = openJevMaskID
	return ot, nil
}

// encodeText tokenizes without adding CLS/SEP (matches HF add_special_tokens=false).
func (t *openJevTokenizer) encodeText(text string) ([]int64, error) {
	if t == nil || t.tok == nil {
		return nil, fmt.Errorf("open-jev tokenizer is nil")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	ids, err := t.tok.Encode(text)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out, nil
}

// stateToText flattens Ask state into the string OpenJev reads.
func stateToText(state any) (string, error) {
	switch typed := state.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	case fmt.Stringer:
		return typed.String(), nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("encode state: %w", err)
		}
		return string(raw), nil
	}
}

// instructionText extracts a string from Question.Instructions.
func instructionText(instructions any) string {
	switch typed := instructions.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}
