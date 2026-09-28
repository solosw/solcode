package organizer

import "strings"

// organizeGrammar is a GBNF grammar constraining decoding to the organizer's
// JSON shape.
//
// Constrained decoding is what makes a small local model usable here: rather
// than hoping a 3B model emits parseable JSON and writing ever more tolerant
// parsers, the sampler is limited to tokens that keep the output well-formed.
// The grammar enforces structure and the enum values, but not the prose, which
// is exactly the split we want — the model chooses content, the grammar
// guarantees shape.
//
// Reference: https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md
const organizeGrammar = `root ::= "{" ws session-summary ws "," ws keywords ws "," ws importance ws "," ws candidates ws "}"

session-summary ::= "\"session_summary\"" ws ":" ws string
keywords ::= "\"keywords\"" ws ":" ws string-array
importance ::= "\"importance\"" ws ":" ws number
candidates ::= "\"candidate_memories\"" ws ":" ws "[" ws candidate-list? ws "]"

candidate-list ::= candidate (ws "," ws candidate)*
candidate ::= "{" ws kind ws "," ws scope ws "," ws tier ws "," ws confidence ws "," ws text ws "," ws tags ws "," ws reason ws "}"

kind ::= "\"kind\"" ws ":" ws kind-value
kind-value ::= "\"fact\"" | "\"preference\"" | "\"constraint\"" | "\"task\"" | "\"workflow\""

scope ::= "\"scope\"" ws ":" ws scope-value
scope-value ::= "\"session\"" | "\"project\"" | "\"global\""

tier ::= "\"suggested_tier\"" ws ":" ws tier-value
tier-value ::= "\"M1\"" | "\"M2\"" | "\"M3\"" | "\"M4\"" | "\"M5\""

confidence ::= "\"confidence\"" ws ":" ws number
text ::= "\"canonical_text\"" ws ":" ws string
tags ::= "\"tags\"" ws ":" ws string-array
reason ::= "\"reason\"" ws ":" ws string

string-array ::= "[" ws (string (ws "," ws string)*)? ws "]"

number ::= "0" | "1" | "0." [0-9]+ | "1.0" | fraction
fraction ::= [0-9] "." [0-9]+

string ::= "\"" char* "\""
char ::= [^"\\\x7F\x00-\x1F] | "\\" (["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F])
ws ::= [ \t\n\r]*`

// OrganizeGrammar returns the GBNF grammar for the organizer output.
//
// It returns a trimmed copy so callers cannot mutate the shared constant.
func OrganizeGrammar() string {
	return strings.TrimSpace(organizeGrammar)
}

// GrammarAvailable reports whether constrained decoding is wired up. It exists
// so callers can log which path they are on without probing the runtime.
func GrammarAvailable() bool {
	return OrganizeGrammar() != ""
}
