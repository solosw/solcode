package memory

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type RetrievalPlan struct {
	Query             string
	SessionID         string
	AllowCrossSession bool
	TotalLimit        int
	M2Limit           int
	M3Limit           int
	M4Limit           int
	M5Limit           int
}

type LayeredRetriever struct{}

type retrievalProfile struct {
	Query           string
	Terms           []string
	Paths           []string
	Basenames       []string
	Extensions      []string
	Symbols         []string
	Commands        []string
	Vector          map[string]float64
	WorkflowQuery   bool
	FileQuery       bool
	CodeQuery       bool
	ValidationQuery bool
}

var (
	retrievalPathPattern    = regexp.MustCompile(`(?i)(?:[A-Za-z]:)?[\w.\-]+(?:[/\\][\w.\-]+)+(?:\.[A-Za-z0-9]+)?`)
	retrievalCommandPattern = regexp.MustCompile(`(?i)\b(?:go test|go build|gofmt|npm test|npm run|pnpm test|pytest|cargo test|make)\b[^\n;。]*`)
	retrievalSymbolPattern  = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?\b`)
)

func (r LayeredRetriever) Retrieve(items []Item, plan RetrievalPlan) []Item {
	if plan.TotalLimit <= 0 {
		plan.TotalLimit = 8
	}
	if plan.M2Limit <= 0 {
		plan.M2Limit = 4
	}
	if plan.M3Limit <= 0 {
		plan.M3Limit = 3
	}
	if plan.M4Limit <= 0 {
		plan.M4Limit = 3
	}
	if plan.M5Limit <= 0 {
		plan.M5Limit = 2
	}
	profile := analyzeRetrievalQuery(plan.Query)
	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if item.Tier == TierSensory {
			continue
		}
		if item.Tier == TierWorking && item.SourceSessionID != "" && item.SourceSessionID != plan.SessionID {
			continue
		}
		if !plan.AllowCrossSession && item.SourceSessionID != "" && item.SourceSessionID != plan.SessionID {
			continue
		}
		filtered = append(filtered, item)
	}
	grouped := map[Tier][]Item{}
	for _, item := range filtered {
		grouped[item.Tier] = append(grouped[item.Tier], item)
	}
	for tier := range grouped {
		grouped[tier] = sortByTierRelevance(grouped[tier], profile)
	}
	result := make([]Item, 0, plan.TotalLimit)
	appendTier := func(tier Tier, limit int) {
		for _, item := range grouped[tier] {
			if len(result) >= plan.TotalLimit || limit <= 0 {
				return
			}
			if containsItem(result, item.ID) {
				continue
			}
			result = append(result, item)
			limit--
		}
	}
	appendTier(TierWorking, plan.M2Limit)
	if profile.WorkflowQuery {
		appendTier(TierProcedural, plan.M5Limit)
	}
	appendTier(TierShortTerm, plan.M3Limit)
	appendTier(TierLongTerm, plan.M4Limit)
	if !profile.WorkflowQuery {
		appendTier(TierProcedural, max(1, plan.M5Limit/2))
	}
	// Fill remaining capacity by global score across all non-selected items.
	if len(result) < plan.TotalLimit {
		rest := make([]Item, 0, len(filtered))
		for _, item := range filtered {
			if containsItem(result, item.ID) {
				continue
			}
			rest = append(rest, item)
		}
		rest = sortByTierRelevance(rest, profile)
		for _, item := range rest {
			if len(result) >= plan.TotalLimit {
				break
			}
			result = append(result, item)
		}
	}
	return result
}

func sortByTierRelevance(items []Item, profile retrievalProfile) []Item {
	out := append([]Item(nil), items...)
	now := time.Now()
	sort.SliceStable(out, func(i, j int) bool {
		si := layeredScore(out[i], profile, now)
		sj := layeredScore(out[j], profile, now)
		if si == sj {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return si > sj
	})
	return out
}

func layeredScore(item Item, profile retrievalProfile, now time.Time) float64 {
	text := strings.ToLower(item.Text + " " + strings.Join(item.Tags, " "))
	score := EvolvedImportance(item, now, 0.05, 0.1)
	for _, term := range profile.Terms {
		if strings.Contains(text, term) {
			score += 10
		}
	}
	score += vectorSimilarityScore(item, profile)
	score += contentAwareScore(item, text, profile)
	switch item.Tier {
	case TierWorking:
		score += 6
	case TierShortTerm:
		score += 4
	case TierLongTerm:
		score += 3
	case TierProcedural:
		score += 2
		if profile.WorkflowQuery || item.Kind == KindWorkflow {
			score += 8
		}
	}
	if profile.WorkflowQuery && item.Kind == KindWorkflow {
		score += 5
	}
	if !item.LastAccessedAt.IsZero() {
		score += 1 / (1 + now.Sub(item.LastAccessedAt).Hours()/24)
	}
	return score
}

func vectorSimilarityScore(item Item, profile retrievalProfile) float64 {
	if len(profile.Vector) == 0 {
		return 0
	}
	itemVector := retrievalVector(item.Text + " " + strings.Join(item.Tags, " ") + " " + string(item.Kind) + " " + string(item.Scope))
	cos := cosineSimilarity(profile.Vector, itemVector)
	if cos <= 0 {
		return 0
	}
	return cos * 45
}

func contentAwareScore(item Item, text string, profile retrievalProfile) float64 {
	score := 0.0
	for _, path := range profile.Paths {
		path = strings.ToLower(filepath.ToSlash(path))
		if strings.Contains(normalizeSlashes(text), path) {
			score += 35
		}
	}
	for _, basename := range profile.Basenames {
		if basename != "" && strings.Contains(text, strings.ToLower(basename)) {
			score += 18
		}
	}
	for _, ext := range profile.Extensions {
		if ext != "" && strings.Contains(text, strings.ToLower(ext)) {
			score += 8
		}
	}
	for _, symbol := range profile.Symbols {
		symbol = strings.ToLower(symbol)
		if symbol != "" && strings.Contains(text, symbol) {
			score += 12
		}
	}
	for _, command := range profile.Commands {
		if command != "" && strings.Contains(text, strings.ToLower(command)) {
			score += 20
		}
	}
	if profile.FileQuery {
		if containsAny(text, []string{"file", "files", "path", "code-change", "modifications", "edited", "wrote", "patch"}) || item.Kind == KindTask {
			score += 10
		}
	}
	if profile.CodeQuery {
		if containsAny(text, []string{"code", "function", "struct", "method", "class", "package", "import", "test", "config"}) || containsTag(item, "code-change") {
			score += 10
		}
	}
	if profile.ValidationQuery {
		if containsAny(text, []string{"go test", "go build", "gofmt", "npm test", "pytest", "validation", "build"}) || containsTag(item, "validation") {
			score += 14
		}
	}
	return score
}

func analyzeRetrievalQuery(query string) retrievalProfile {
	lower := strings.ToLower(strings.TrimSpace(query))
	paths := uniqueLowerStrings(retrievalPathPattern.FindAllString(lower, -1))
	commands := uniqueLowerStrings(retrievalCommandPattern.FindAllString(lower, -1))
	basenames := make([]string, 0, len(paths))
	extensions := make([]string, 0, len(paths))
	for _, path := range paths {
		base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
		if base != "." && base != "/" && base != "" {
			basenames = append(basenames, base)
		}
		if ext := filepath.Ext(base); ext != "" {
			extensions = append(extensions, ext)
		}
	}
	terms := queryTerms(query)
	symbols := codeSymbols(lower, terms)
	fileQuery := len(paths) > 0 || containsAny(lower, []string{"文件", "路径", "file", "path", "改了", "修改", "edited", "write", "patch"})
	codeQuery := fileQuery || len(symbols) > 0 || containsAny(lower, []string{"代码", "函数", "结构体", "方法", "测试", "code", "function", "struct", "method", "class", "test", "config"})
	validationQuery := len(commands) > 0 || containsAny(lower, []string{"测试", "验证", "构建", "test", "build", "gofmt", "pytest", "validation"})
	return retrievalProfile{
		Query:           query,
		Terms:           terms,
		Paths:           paths,
		Basenames:       uniqueLowerStrings(basenames),
		Extensions:      uniqueLowerStrings(extensions),
		Symbols:         symbols,
		Commands:        commands,
		Vector:          retrievalVector(query),
		WorkflowQuery:   isWorkflowQuery(query),
		FileQuery:       fileQuery,
		CodeQuery:       codeQuery,
		ValidationQuery: validationQuery,
	}
}

func retrievalVector(text string) map[string]float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	vec := map[string]float64{}
	add := func(token string, weight float64) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" || isRetrievalStopword(token) {
			return
		}
		vec[token] += weight
	}
	lower := strings.ToLower(text)
	for _, path := range retrievalPathPattern.FindAllString(lower, -1) {
		path = strings.ToLower(filepath.ToSlash(path))
		add("path:"+path, 4)
		base := filepath.Base(path)
		add("file:"+base, 3)
		if ext := filepath.Ext(base); ext != "" {
			add("ext:"+ext, 1.5)
		}
		for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' || r == '.' || r == '-' || r == '_' }) {
			add(part, 1.2)
		}
	}
	for _, command := range retrievalCommandPattern.FindAllString(lower, -1) {
		add("cmd:"+strings.Join(strings.Fields(command), " "), 3)
		for _, part := range strings.Fields(command) {
			add(part, 1.2)
		}
	}
	for _, token := range lexicalTokens(text) {
		weight := 1.0
		if strings.Contains(token, "_") || strings.Contains(token, ".") {
			weight = 1.8
		}
		add(token, weight)
		for _, part := range splitCodeToken(token) {
			add(part, 0.9)
		}
	}
	for _, gram := range cjkBigrams(text) {
		add(gram, 1.4)
	}
	return vec
}

func lexicalTokens(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '.' && r != '-' && r != '/' && r != '\\'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(strings.ToLower(strings.TrimSpace(field)), ".-/\\")
		if len([]rune(field)) < 2 {
			continue
		}
		out = append(out, field)
	}
	return out
}

func splitCodeToken(token string) []string {
	parts := strings.FieldsFunc(token, func(r rune) bool {
		return r == '_' || r == '.' || r == '-' || r == '/' || r == '\\'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) >= 2 {
			out = append(out, part)
		}
	}
	return out
}

func cjkBigrams(text string) []string {
	var chars []rune
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			chars = append(chars, r)
			continue
		}
		if len(chars) > 0 {
			chars = append(chars, ' ')
		}
	}
	segments := strings.Fields(string(chars))
	var grams []string
	for _, segment := range segments {
		runes := []rune(segment)
		if len(runes) == 1 {
			grams = append(grams, string(runes[0]))
			continue
		}
		for i := 0; i < len(runes)-1; i++ {
			grams = append(grams, string(runes[i:i+2]))
		}
	}
	return grams
}

func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	dot := 0.0
	for token, av := range a {
		if bv := b[token]; bv != 0 {
			dot += av * bv
		}
	}
	if dot == 0 {
		return 0
	}
	return dot / (vectorNorm(a) * vectorNorm(b))
}

func vectorNorm(vec map[string]float64) float64 {
	sum := 0.0
	for _, value := range vec {
		sum += value * value
	}
	if sum <= 0 {
		return 1
	}
	return math.Sqrt(sum)
}

func isRetrievalStopword(token string) bool {
	switch token {
	case "the", "and", "for", "with", "this", "that", "what", "when", "where", "which", "about", "into", "from", "了", "的", "一下", "继续", "看看", "这个", "那个":
		return true
	}
	return false
}

func codeSymbols(lower string, terms []string) []string {
	seen := map[string]bool{}
	for _, term := range terms {
		if strings.Contains(term, "_") || strings.Contains(term, ".") || hasMixedCase(term) {
			seen[term] = true
		}
	}
	for _, symbol := range retrievalSymbolPattern.FindAllString(lower, -1) {
		if len(symbol) >= 4 && (strings.Contains(symbol, "_") || strings.Contains(symbol, ".")) {
			seen[symbol] = true
		}
	}
	out := make([]string, 0, len(seen))
	for symbol := range seen {
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}

func isWorkflowQuery(query string) bool {
	lower := strings.ToLower(strings.TrimSpace(query))
	markers := []string{"怎么做", "步骤", "流程", "workflow", "how to", "how do", "first", "next", "review"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func containsItem(items []Item, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func containsTag(item Item, tag string) bool {
	for _, value := range item.Tags {
		if strings.EqualFold(value, tag) {
			return true
		}
	}
	return false
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func uniqueLowerStrings(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeSlashes(text string) string {
	return strings.ReplaceAll(text, "\\", "/")
}

func hasMixedCase(text string) bool {
	hasLower := false
	hasUpper := false
	for _, r := range text {
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
	}
	return hasLower && hasUpper
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
