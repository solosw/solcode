package organizer

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// maxTranscriptRunes bounds what is sent to the model. With a 16k context
	// window there is room for a long compact transcript after system +
	// side-context + ~800 generation headroom. Truncation keeps head (intent)
	// and tail (outcome).
	maxTranscriptRunes = 24000
	// maxCandidateRunes bounds one stored candidate text.
	maxCandidateRunes = 600
	// maxKeywords caps returned session keywords.
	maxKeywords = 12
	// maxSummaryRunes bounds the returned session summary and the previous/
	// next summary fields stuffed into the user payload.
	maxSummaryRunes = 4000
)

const organizeSystemPrompt = `You organize a finished coding-agent session into durable memory.

Return ONLY one XML document. No prose, no markdown fences, no commentary, no JSON.

Required shape:
<result>
  <session_summary>problem + status summary (see rules)</session_summary>
  <keywords>
    <k>short</k>
    <k>retrieval</k>
    <k>terms</k>
  </keywords>
  <importance>0.0</importance>
  <candidate_memories>
    <candidate>
      <kind>fact|preference|constraint|task|workflow</kind>
      <scope>session|project|global</scope>
      <suggested_tier>M1|M2|M3|M4|M5</suggested_tier>
      <confidence>0.0</confidence>
      <canonical_text>one self-contained sentence</canonical_text>
      <tags>
        <t>optional</t>
      </tags>
      <reason>why this is worth remembering</reason>
      <status>active|superseded|expired|contradicted</status>
      <supersedes>optional older memory id or short topic key</supersedes>
    </candidate>
  </candidate_memories>
</result>

Rules:
- session_summary MUST cover both of these, in plain prose (2-6 short
  sentences; keep the ending complete — never stop mid-word):
  1) Problem: the user's actual request / bug / goal that this turn or compact
     was about.
  2) Status: what was fixed or decided, how it was verified (tests/build when
     present), and what is still unfinished or blocked.
  Also preserve exact file paths and symbols when they matter, validation
  results, and errors. No role prefixes, no tool-call JSON, no code listings.
- Ground truth when present: changed_files, todos, tool_facts, and
  related_memories beat vague chat prose. Prefer exact paths and commands from
  those fields. Use todos for unfinished work.
- candidate_memories: only durable knowledge that stays true beyond this
  session. Prefer user preferences, project rules, verified commands, and
  settled decisions. Do not restate the summary as a candidate.
- Every canonical_text must be understandable on its own, without the session.
- status defaults to active. Use superseded only when this candidate replaces a
  known older rule; put that older id or a short topic key in supersedes.
  When related_memories show an older conflicting rule, supersede it.
- Never include secrets: no API keys, tokens, passwords, private keys, or
  authorization headers. Skip such an item entirely rather than redacting it.
- Return an empty <candidate_memories></candidate_memories> when the session
  produced no durable knowledge. That is a normal and expected outcome.
- importance is between 0 and 1.
- Element text must not contain raw < or > characters. Prefer plain prose.`

const (
	maxPayloadListItems = 12
	maxPayloadItemRunes = 240
	maxRelatedRunes     = 180
)

// buildUserPayload assembles the model input. It is plain JSON so the model sees
// an unambiguous boundary between structured side-context and the transcript.
func buildUserPayload(input Input, transcript string) string {
	payload := map[string]any{
		"session_id": input.SessionID,
		"work_dir":   input.WorkDir,
		"transcript": transcript,
	}
	if trigger := strings.TrimSpace(input.Trigger); trigger != "" {
		payload["trigger"] = trigger
	}
	if prev := strings.TrimSpace(input.PreviousSummary); prev != "" {
		payload["previous_summary"] = truncateRunes(prev, maxSummaryRunes)
	}
	if next := strings.TrimSpace(input.NextSummary); next != "" {
		payload["next_summary"] = truncateRunes(next, maxSummaryRunes)
	}
	if files := sanitizePayloadList(input.ChangedFiles, maxPayloadListItems, 120); len(files) > 0 {
		payload["changed_files"] = files
	}
	if todos := sanitizePayloadList(input.Todos, maxPayloadListItems, maxPayloadItemRunes); len(todos) > 0 {
		payload["todos"] = todos
	}
	if facts := sanitizePayloadList(input.ToolFacts, maxPayloadListItems, maxPayloadItemRunes); len(facts) > 0 {
		payload["tool_facts"] = facts
	}
	if related := sanitizePayloadList(input.RelatedMemories, 8, maxRelatedRunes); len(related) > 0 {
		payload["related_memories"] = related
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshal of a map of strings cannot fail in practice; fall back to the
		// transcript so a surprise never costs the whole run.
		return transcript
	}
	return string(raw)
}

func sanitizePayloadList(values []string, limit, maxRunes int) []string {
	if limit <= 0 {
		limit = maxPayloadListItems
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if value == "" {
			continue
		}
		value = truncateRunes(value, maxRunes)
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// xmlResult is the wire shape the model is asked to emit.
type xmlResult struct {
	XMLName        xml.Name      `xml:"result"`
	SessionSummary string        `xml:"session_summary"`
	Keywords       xmlKeywords   `xml:"keywords"`
	Importance     string        `xml:"importance"`
	Candidates     xmlCandidates `xml:"candidate_memories"`
}

type xmlKeywords struct {
	Items []string `xml:"k"`
}

type xmlCandidates struct {
	Items []xmlCandidate `xml:"candidate"`
}

type xmlCandidate struct {
	Kind       string  `xml:"kind"`
	Scope      string  `xml:"scope"`
	Tier       string  `xml:"suggested_tier"`
	Confidence string  `xml:"confidence"`
	Text       string  `xml:"canonical_text"`
	Tags       xmlTags `xml:"tags"`
	Reason     string  `xml:"reason"`
	Status     string  `xml:"status"`
	Supersedes string  `xml:"supersedes"`
}

type xmlTags struct {
	Items []string `xml:"t"`
}

// ParseResult validates raw model output into a Result.
//
// Parsing is deliberately tolerant about the envelope and strict about the
// content: a local model may wrap its XML in a fenced block or add a stray
// sentence, but a candidate with an unknown kind, a sensitive payload, or no
// text is dropped rather than stored. A missing summary is an error because the
// caller needs it to compact safely.
func ParseResult(raw string) (Result, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Result{}, fmt.Errorf("organizer: empty model response")
	}
	block, ok := extractXMLDocument(text)
	if !ok {
		return Result{}, fmt.Errorf("organizer: no XML result in model response")
	}
	var parsed xmlResult
	if err := xml.Unmarshal([]byte(block), &parsed); err != nil {
		return Result{}, fmt.Errorf("organizer: decode model response: %w", err)
	}

	summary := sanitizeSummary(parsed.SessionSummary)
	if summary == "" {
		return Result{}, fmt.Errorf("organizer: model returned no session summary")
	}

	result := Result{
		Summary:    summary,
		Keywords:   sanitizeKeywords(parsed.Keywords.Items),
		Importance: clampUnit(parseUnit(parsed.Importance, 0.5)),
	}
	for _, candidate := range parsed.Candidates.Items {
		if next, ok := sanitizeCandidate(candidate); ok {
			result.Candidates = append(result.Candidates, next)
		}
	}
	return result, nil
}

// sanitizeCandidate validates one candidate, returning false when it must be
// dropped.
func sanitizeCandidate(raw xmlCandidate) (Candidate, bool) {
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
		Confidence: clampUnit(parseUnit(raw.Confidence, 0.7)),
		Text:       text,
		Tags:       sanitizeKeywords(raw.Tags.Items),
		Reason:     truncateRunes(strings.TrimSpace(raw.Reason), 200),
		Status:     normalizeStatus(raw.Status),
		Supersedes: sanitizeSupersedes(raw.Supersedes),
	}, true
}

func normalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "superseded", "expired", "contradicted":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "active"
	}
}

func sanitizeSupersedes(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Keep a short id/topic key only — no free-form prose that could bloat storage.
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, 80)
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

// xmlFencePattern matches a ```xml ... ``` or ``` ... ``` block that holds
// a <result> document.
var xmlFencePattern = regexp.MustCompile("(?s)```(?:xml)?\\s*(<result\\b.*?</result>)\\s*```")

// extractXMLDocument finds the outermost <result>...</result> in a model response.
//
// A local model may emit the document bare, inside a fenced block, or surrounded
// by a sentence. All three are accepted; anything with no result root is rejected.
func extractXMLDocument(text string) (string, bool) {
	if match := xmlFencePattern.FindStringSubmatch(text); len(match) == 2 {
		return match[1], true
	}
	lower := strings.ToLower(text)
	start := strings.Index(lower, "<result")
	if start < 0 {
		return "", false
	}
	// Find the matching close tag from the end so nested noise is less likely
	// to truncate a legitimate document.
	end := strings.LastIndex(lower, "</result>")
	if end < start {
		return "", false
	}
	end += len("</result>")
	return text[start:end], true
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

func parseUnit(value string, fallback float64) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return n
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
