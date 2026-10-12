// Package projectindex supplies a bounded, local repository map. It makes no
// model requests: paths and symbols are discovered with git and local file
// reads only.
package projectindex

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const maxPaths = 10000

// maxSymbolFileBytes bounds how much of any one file is scanned for symbols.
const maxSymbolFileBytes = 256 * 1024

// maxSymbolScanFiles bounds how many candidate files a symbol search reads so a
// huge repository cannot turn discovery into a full-tree scan.
const maxSymbolScanFiles = 400

// RelevantPaths returns likely relevant project-relative paths. Git determines
// tracked/ignored paths where possible; the fallback never descends into common
// generated/vendor directories.
func RelevantPaths(ctx context.Context, root, query string, limit int) []string {
	if strings.TrimSpace(root) == "" || limit <= 0 {
		return nil
	}
	if limit > 40 {
		limit = 40
	}
	entries := gitPaths(ctx, root)
	if len(entries) == 0 {
		entries = walkPaths(root)
	}
	if len(entries) == 0 {
		return nil
	}
	terms := queryTerms(query)
	type scored struct {
		path  string
		score int
	}
	ranked := make([]scored, 0, len(entries))
	for _, p := range entries {
		lower := strings.ToLower(p)
		base := strings.ToLower(filepath.Base(p))
		score := 0
		for _, term := range terms {
			if term == "" {
				continue
			}
			if strings.Contains(base, term) {
				score += 8
			} else if strings.Contains(lower, term) {
				score += 3
			}
		}
		if strings.HasPrefix(lower, "internal/") || strings.HasPrefix(lower, "src/") {
			score++
		}
		if strings.HasSuffix(lower, "_test.go") || strings.Contains(lower, "/test/") {
			score--
		}
		ranked = append(ranked, scored{p, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].path < ranked[j].path
	})
	result := make([]string, 0, limit)
	for _, p := range ranked {
		if len(result) >= limit {
			break
		}
		result = append(result, p.path)
	}
	return result
}

func queryTerms(query string) []string {
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' && r != '/'
	})
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if len(t) >= 3 && len(t) < 80 {
			out = append(out, t)
		}
	}
	return out
}

func gitPaths(ctx context.Context, root string) []string {
	sub, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(sub, "git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil || len(out) > 2*1024*1024 {
		return nil
	}
	paths := make([]string, 0, 1024)
	seen := map[string]bool{}
	for _, p := range strings.Split(string(out), "\n") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || strings.ContainsAny(p, "\r\x00") || strings.HasPrefix(p, "../") {
			continue
		}
		seen[p] = true
		paths = append(paths, filepath.ToSlash(p))
		if len(paths) >= maxPaths {
			break
		}
	}
	return paths
}

func walkPaths(root string) []string {
	paths := make([]string, 0, 1024)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(paths) >= maxPaths {
			return filepath.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", "vendor", "dist", "build", ".next", ".venv", "coverage":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e == nil {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	return paths
}

// SymbolHit is one code-symbol match found by a local text scan.
type SymbolHit struct {
	Path string
	Line int
	Kind string
	Name string
}

// symbolPatterns match common declaration forms across languages. They are
// deliberately lexical (not a real parser): the goal is bounded discovery that a
// later View/read confirms, not a syntax-accurate index.
var symbolPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"func", regexp.MustCompile(`^\s*(?:async\s+)?func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)},
	{"func", regexp.MustCompile(`^\s*(?:def|function|fn)\s+([A-Za-z_][A-Za-z0-9_]*)`)},

	{"type", regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)`)},
	{"class", regexp.MustCompile(`^\s*(?:class|interface|struct|enum)\s+([A-Za-z_][A-Za-z0-9_]*)`)},
	{"const", regexp.MustCompile(`^\s*(?:const|var|let)\s+([A-Za-z_][A-Za-z0-9_]*)`)},
}

// SearchSymbols scans candidate files for declarations whose name contains the
// query term. It is bounded by file size, file count and result count, and never
// leaves root. Results are sorted by path then line.
func SearchSymbols(ctx context.Context, root, query string, limit int) []SymbolHit {
	query = strings.TrimSpace(query)
	if root == "" || query == "" || limit <= 0 {
		return nil
	}
	if limit > 40 {
		limit = 40
	}
	needle := strings.ToLower(query)
	entries := gitPaths(ctx, root)
	if len(entries) == 0 {
		entries = walkPaths(root)
	}
	if len(entries) == 0 {
		return nil
	}
	// Prefer source-like files and cap how many are opened.
	candidates := make([]string, 0, len(entries))
	for _, p := range entries {
		if isSourcePath(p) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) > maxSymbolScanFiles {
		candidates = candidates[:maxSymbolScanFiles]
	}
	hits := make([]SymbolHit, 0, limit)
	for _, rel := range candidates {
		if len(hits) >= limit {
			break
		}
		if ctx.Err() != nil {
			break
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		scanFileSymbols(abs, rel, needle, limit, &hits)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Line < hits[j].Line
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func scanFileSymbols(abs, rel, needle string, limit int, hits *[]SymbolHit) {
	f, err := os.Open(abs)
	if err != nil {
		return
	}
	defer f.Close()
	reader := bufio.NewReader(io.LimitReader(f, maxSymbolFileBytes))
	line := 0
	for {
		text, err := reader.ReadString('\n')
		if text != "" {
			line++
			trimmed := strings.TrimRight(text, "\r\n")
			for _, pattern := range symbolPatterns {
				m := pattern.re.FindStringSubmatch(trimmed)
				if m == nil {
					continue
				}
				name := m[len(m)-1]
				if strings.Contains(strings.ToLower(name), needle) {
					*hits = append(*hits, SymbolHit{Path: rel, Line: line, Kind: pattern.kind, Name: name})
					if len(*hits) >= limit {
						return
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func isSourcePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt",
		".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".rb", ".php", ".swift",
		".scala", ".sh", ".lua", ".m", ".mm":
		return true
	default:
		return false
	}
}

// RecentPaths returns the most recently modified tracked paths, newest first.
// Bounded by how many entries are stat-ed.
func RecentPaths(ctx context.Context, root string, limit int) []string {
	if root == "" || limit <= 0 {
		return nil
	}
	if limit > 40 {
		limit = 40
	}
	entries := gitPaths(ctx, root)
	if len(entries) == 0 {
		entries = walkPaths(root)
	}
	type dated struct {
		path string
		mod  time.Time
	}
	datedEntries := make([]dated, 0, limit)
	for _, p := range entries {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		datedEntries = append(datedEntries, dated{p, info.ModTime()})
	}
	sort.Slice(datedEntries, func(i, j int) bool {
		if !datedEntries[i].mod.Equal(datedEntries[j].mod) {
			return datedEntries[i].mod.After(datedEntries[j].mod)
		}
		return datedEntries[i].path < datedEntries[j].path
	})
	out := make([]string, 0, limit)
	for _, d := range datedEntries {
		if len(out) >= limit {
			break
		}
		out = append(out, d.path)
	}
	return out
}

// Context returns a small, untrusted-data-labelled candidate-file index. When
// the query names a likely symbol, a bounded local text scan adds declaration
// hints so the model can go straight to the definition.
func Context(ctx context.Context, root, query string) string {
	paths := RelevantPaths(ctx, root, query, 20)
	var b strings.Builder
	if len(paths) > 0 {
		b.WriteString("Repository candidate paths (local path index; verify contents using BatchTool):\n- ")
		b.WriteString(strings.Join(paths, "\n- "))
	}
	if symbols := SearchSymbols(ctx, root, symbolQuery(query), 12); len(symbols) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Candidate symbol definitions (local text scan; verify by reading):")
		for _, hit := range symbols {
			b.WriteString("\n- " + hit.Kind + " " + hit.Name + " — " + hit.Path + ":" + itoa(hit.Line))
		}
	}
	return b.String()
}

// symbolQuery picks the most symbol-like term from a free-text query.
func symbolQuery(query string) string {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return ""
	}
	best := terms[0]
	for _, t := range terms {
		if len(t) > len(best) {
			best = t
		}
	}
	return best
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
