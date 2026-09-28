package organizer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// maxTranscriptRunes bounds what is sent to the model. A local 3B model on
	// CPU degrades badly past its context window, so the transcript is
	// truncated from the middle-forward rather than the tail: the head carries
	// the intent, the tail carries the outcome.
	maxTranscriptRunes = 24000
	// maxCandidateRunes bounds one stored candidate text.
	maxCandidateRunes = 600
	// maxKeywords caps returned session keywords.
	maxKeywords = 12
	// maxSummaryRunes bounds the returned session summary.
	maxSummaryRunes = 4000
)

const organizeSystemPrompt = `You organize a finished coding-agent session into durable memory.

Return ONLY one JSON object. No prose, no markdown fences, no commentary.

Required shape:
{
  "session_summary": "concise factual summary of what the session did",
  "keywords": ["short", "retrieval", "terms"],
  "importance": 0.0,
  "candidate_memories": [
    {
      "kind": "fact|preference|constraint|task|workflow",
      "scope": "session|project|global",
      "suggested_tier": "M1|M2|M3|M4|M5",
      "confidence": 0.0,
      "canonical_text": "one self-contained sentence",
      "tags": ["optional"],
      "reason": "why this is worth remembering"
    }
  ]
}

Rules:
- session_summary: preserve the user's actual request, decisions, exact file
  paths and symbols when they matter, validation results, errors, and any
  unfinished work. No role prefixes, no tool-call JSON, no code listings.
- candidate_memories: only durable knowledge that stays true beyond this
  session. Prefer user preferences, project rules, verified commands, and
  settled decisions. Do not restate the summary as a candidate.
- Every canonical_text must be understandable on its own, without the session.
- Never include secrets: no API keys, tokens, passwords, private keys, or
  authorization headers. Skip such an item entirely rather than redacting it.
- Return an empty candidate_memories array when the session produced no durable
  knowledge. That is a normal and expected outcome.
- importance is between 0 and 1.`

// buildUserPayload assembles the model input. It is plain JSON so the model sees
// an unambiguous boundary between the previous summary and the new transcript.
func buildUserPayload(input Input, transcript string) string {
	payload := map[string]any{
		"session_id": input.SessionID,
		"work_dir":   input.WorkDir,
		"transcript": transcript,
	}
	if prev := strings.TrimSpace(input.PreviousSummary); prev != "" {
		payload["previous_summary"] = truncateRunes(prev, maxSummaryRunes)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshal of a map of strings cannot fail in practice; fall back to the
		// transcript so a surprise never costs the whole run.
		return transcript
	}
	return string(raw)
}

// rawResult is the wire shape the model is asked to emit. Fields are pointers
// or loosely typed so a partially malformed response can still yield the parts
// that did parse.
type rawResult struct {
	SessionSummary string         `json:"session_summary"`
	Keywords       []string       `json:"keywords"`
	Importance     *float64       `json:"importance"`
	Candidates     []rawCandidate `json:"candidate_memories"`
}

type rawCandidate struct {
	Kind       string   `json:"kind"`
	Scope      string   `json:"scope"`
	Tier       string   `json:"suggested_tier"`
	Confidence *float64 `json:"confidence"`
	Text       string   `json:"canonical_text"`
	Tags       []string `json:"tags"`
	Reason     string   `json:"reason"`
}

// ParseResult validates raw model output into a Result.
//
// Parsing is deliberately tolerant about the envelope and strict about the
// content: a local model may wrap its JSON in a fenced block or add a stray
// sentence, but a candidate with an unknown kind, a sensitive payload, or no
// text is dropped rather than stored. A missing summary is an error because the
// caller needs it to compact safely.
func ParseResult(raw string) (Result, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Result{}, fmt.Errorf("organizer: empty model response")
	}
	block, ok := extractJSONObject(text)
	if !ok {
		return Result{}, fmt.Errorf("organizer: no JSON object in model response")
	}
	var parsed rawResult
	if err := json.Unmarshal([]byte(block), &parsed); err != nil {
		return Result{}, fmt.Errorf("organizer: decode model response: %w", err)
	}

	summary := sanitizeSummary(parsed.SessionSummary)
	if summary == "" {
		return Result{}, fmt.Errorf("organizer: model returned no session summary")
	}

	result := Result{
		Summary:    summary,
		Keywords:   sanitizeKeywords(parsed.Keywords),
		Importance: clampUnit(valueOr(parsed.Importance, 0.5)),
	}
	for _, candidate := range parsed.Candidates {
		if next, ok := sanitizeCandidate(candidate); ok {
			result.Candidates = append(result.Candidates, next)
		}
	}
	return result, nil
}

// sanitizeCandidate validates one candidate, returning false when it must be
// dropped.
func sanitizeCandidate(raw rawCandidate) (Candidate, bool) {
	text := sanitizeCandidateText(raw.Text)
	if text == "" {
		return Candidate{}, false
	}
	// Reject secrets outright. The model is told to skip them, but a local model
	// does not always comply and a stored secret is unrecoverable.
	if looksSensitive(text) {
		return Candidate{}, false
	}
	kind := normalizeKind(raw.Kind)
	if kind == "" {
		return Candidate{}, false
	}
	tier := normalizeTier(raw.Tier)
	if tier == "" {
		return Candidate{}, false
	}
	return Candidate{
		Kind:       kind,
		Scope:      normalizeScope(raw.Scope),
		Tier:       tier,
		Confidence: clampUnit(valueOr(raw.Confidence, 0.7)),
		Text:       text,
		Tags:       sanitizeKeywords(raw.Tags),
		Reason:     truncateRunes(strings.TrimSpace(raw.Reason), 200),
	}, true
}

func normalizeKind(value string) CandidateKind {
	switch CandidateKind(strings.ToLower(strings.TrimSpace(value))) {
	case CandidateFact:
		return CandidateFact
	case CandidatePreference:
		return CandidatePreference
	case CandidateConstraint:
		return CandidateConstraint
	case CandidateTask:
		return CandidateTask
	case CandidateWorkflow:
		return CandidateWorkflow
	default:
		// An unlabelled candidate is more likely a fact than noise, but the
		// kind is what memory typing depends on, so treat unknown as fact only
		// when the model simply omitted it.
		if strings.TrimSpace(value) == "" {
			return CandidateFact
		}
		return ""
	}
}

func normalizeTier(value string) CandidateTier {
	switch CandidateTier(strings.ToUpper(strings.TrimSpace(value))) {
	case CandidateTierSensory:
		return CandidateTierSensory
	case CandidateTierWorking:
		return CandidateTierWorking
	case CandidateTierShortTerm:
		return CandidateTierShortTerm
	case CandidateTierLongTerm:
		return CandidateTierLongTerm
	case CandidateTierProcedural:
		return CandidateTierProcedural
	default:
		if strings.TrimSpace(value) == "" {
			return CandidateTierShortTerm
		}
		return ""
	}
}

func normalizeScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "session":
		return "session"
	case "global":
		return "global"
	default:
		return "project"
	}
}

// sanitizeSummary removes the structural noise a local model tends to add.
func sanitizeSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Trim(text, "`")
	lines := nonEmptyLines(text)
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		// Drop role prefixes and tool-call echoes.
		if strings.HasPrefix(lower, "user:") || strings.HasPrefix(lower, "assistant:") {
			line = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
		if strings.HasPrefix(lower, "[tool use") || strings.HasPrefix(lower, "[tool result") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		cleaned = append(cleaned, line)
	}
	if len(cleaned) == 0 {
		return ""
	}
	return truncateRunes(strings.Join(cleaned, "\n"), maxSummaryRunes)
}

func sanitizeCandidateText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "`")
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) < 8 {
		// Too short to be self-contained knowledge.
		return ""
	}
	return truncateRunes(text, maxCandidateRunes)
}

func sanitizeKeywords(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if len(out) >= maxKeywords {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sensitivePatterns mirrors internal/memory.LooksSensitive without importing
// that package (memory imports organizer, so the dependency must stay one-way).
var sensitiveMarkers = []string{
	"api_key", "apikey", "secret", "password", "passwd", "token", "bearer ",
	"sk-", "ghp_", "github_pat_", "anthropic_api_key", "openai_api_key",
	"private key", "authorization:",
}

var longSecretPattern = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)

func looksSensitive(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range sensitiveMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return longSecretPattern.MatchString(text)
}

// jsonFencePattern matches a ```json ... ``` or ``` ... ``` block.
var jsonFencePattern = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// extractJSONObject finds the outermost JSON object in a model response.
//
// A local model may emit the object bare, inside a fenced block, or surrounded
// by a sentence. All three are accepted; anything with no object is rejected.
func extractJSONObject(text string) (string, bool) {
	if match := jsonFencePattern.FindStringSubmatch(text); len(match) == 2 {
		return match[1], true
	}
	start := strings.Index(text, "{")
	if start < 0 {
		return "", false
	}
	end := strings.LastIndex(text, "}")
	if end <= start {
		return "", false
	}
	return text[start : end+1], true
}

func nonEmptyLines(text string) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func valueOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
