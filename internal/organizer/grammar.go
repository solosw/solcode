package organizer

import "strings"

// organizeGrammar is a GBNF grammar constraining decoding to the organizer's
// XML shape.
//
// Constrained decoding is what makes a small local model usable here: rather
// than hoping a 3B model emits parseable markup and writing ever more tolerant
// parsers, the sampler is limited to tokens that keep the output well-formed.
// The grammar enforces structure and the enum values, but not the prose, which
// is exactly the split we want — the model chooses content, the grammar
// guarantees shape.
//
// Text fields are length-bounded, and every field must start with a non-space
// character. Both matter for a 1B model:
//
//   - An unbounded `char*` let MiniCPM loop forever inside <session_summary>
//     (hundreds of repeated transcript fragments, never a closing tag).
//   - `char{1,N}` alone is still not enough: a single space satisfies it, so the
//     model emitted "<session_summary> </session_summary>", and when it instead
//     repeated digits ("2.0 1.0 2.0 3.0 ...") it burned the whole token budget
//     inside <canonical_text> and never closed the document. Whitespace-only
//     fields must be rejected by the grammar, and bounds must sit well below
//     MaxOutputTokens so the document always closes.
//
// Enum-adjacent numbers are constrained to 0..1: the old `fraction` alternative
// accepted any digit before the point, so "2.0" and "64" parsed as valid
// importance/confidence values.
//
// Reference: https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md
const organizeGrammar = `root ::= "<result>" ws session-summary ws keywords ws importance ws candidates ws "</result>"

session-summary ::= "<session_summary>" summary-text "</session_summary>"
keywords ::= "<keywords>" ws keyword-list? ws "</keywords>"
keyword-list ::= keyword (ws keyword){0,5}
keyword ::= "<k>" short-text "</k>"
importance ::= "<importance>" number "</importance>"
candidates ::= "<candidate_memories>" ws candidate-list? ws "</candidate_memories>"

candidate-list ::= candidate (ws candidate){0,5}
candidate ::= "<candidate>" ws kind ws scope ws tier ws confidence ws text-field ws tags ws reason ws status ws supersedes ws "</candidate>"

kind ::= "<kind>" kind-value "</kind>"
kind-value ::= "fact" | "preference" | "constraint" | "task" | "workflow"

scope ::= "<scope>" scope-value "</scope>"
scope-value ::= "session" | "project" | "global"

tier ::= "<suggested_tier>" tier-value "</suggested_tier>"
tier-value ::= "M1" | "M2" | "M3" | "M4" | "M5"

confidence ::= "<confidence>" number "</confidence>"
text-field ::= "<canonical_text>" body-text "</canonical_text>"
tags ::= "<tags>" ws tag-list? ws "</tags>"
tag-list ::= tag (ws tag){0,7}
tag ::= "<t>" short-text "</t>"
reason ::= "<reason>" reason-text "</reason>"
status ::= ( "<status>" status-value? "</status>" ws )?
status-value ::= "active" | "superseded" | "expired" | "contradicted"
supersedes ::= ( "<supersedes>" supersedes-text? "</supersedes>" ws )?
supersedes-text ::= text-char{0,80}

# 0..1 only: no leading digit other than 0, and 1 may only be followed by ".0".
number ::= "0" | "1" | "0." fraction-digits | "1.0"
fraction-digits ::= [0-9]+

# Bounded free text. The first character must be non-space so a field can never
# be satisfied by whitespace alone; the rest may contain spaces. Bounds are kept
# tight on purpose: the whole document must still close inside MaxOutputTokens.
# MiniCPM-1B burned an 800-token budget by repeating one keyword 12 times, so
# repetition limits are small and text caps sit far below the token ceiling.
summary-text ::= text-start text-char{0,239}
body-text ::= text-start text-char{7,159}
reason-text ::= text-char{0,120}
short-text ::= text-start text-char{0,19}
text-start ::= [^ <>&\x7F\x00-\x1F]
text-char ::= [^<>&\x7F\x00-\x1F]
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
