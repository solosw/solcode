package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultFingerprintMaxFiles caps workdir walks for opaque mutators (Bash).
	DefaultFingerprintMaxFiles = 5000
	// DefaultFingerprintMaxBytes skips individual files larger than this.
	DefaultFingerprintMaxBytes = 1 << 21 // 1 MiB
)

// FingerprintCheckpointTool reports tools that mutate files without declaring
// paths, so checkpoints use before/after content-hash fingerprints.
func FingerprintCheckpointTool(name string) bool {
	switch name {
	case BashToolName:
		return true
	default:
		return false
	}
}

// FingerprintOptions controls workdir fingerprint walks.
type FingerprintOptions struct {
	MaxFiles int
	MaxBytes int64
}

func (o FingerprintOptions) withDefaults() FingerprintOptions {
	if o.MaxFiles <= 0 {
		o.MaxFiles = DefaultFingerprintMaxFiles
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultFingerprintMaxBytes
	}
	return o
}

// FileFingerprint is one workdir-relative file's content hash.
// Content is populated for before-snapshots so Capture can store turn-start bytes.
type FileFingerprint struct {
	Hash    string
	Content *string
	Exists  bool
}

// FingerprintChange is a path whose hash (or existence) changed.
type FingerprintChange struct {
	Path    string
	Content *string // turn-start / before content; nil means file was absent
}

// HashBytes returns the SHA-256 hex digest of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SnapshotWorkDir walks workDir and fingerprints text-ish files under the caps.
// Keys are slash-separated paths relative to workDir. keepContent controls whether
// file bodies are retained (needed for before-snapshots that feed Capture).
func SnapshotWorkDir(workDir string, opts FingerprintOptions, keepContent bool) (map[string]FileFingerprint, error) {
	workDir = filepath.Clean(strings.TrimSpace(workDir))
	if workDir == "" {
		return nil, nil
	}
	opts = opts.withDefaults()
	out := make(map[string]FileFingerprint)
	count := 0
	err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info == nil {
			return nil
		}
		if info.IsDir() {
			if path != workDir && shouldSkipFingerprintDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= opts.MaxFiles {
			return filepath.SkipAll
		}
		if SkipHiddenPath(path) || shouldSkipFingerprintFile(path, info.Size(), opts.MaxBytes) {
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" || strings.HasPrefix(rel, "../") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if looksBinary(body) {
			return nil
		}
		fp := FileFingerprint{
			Hash:   HashBytes(body),
			Exists: true,
		}
		if keepContent {
			text := string(body)
			fp.Content = &text
		}
		out[rel] = fp
		count++
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return out, err
	}
	return out, nil
}

// DiffFingerprints returns Capture payloads for paths whose hash/existence changed.
func DiffFingerprints(before, after map[string]FileFingerprint) []FingerprintChange {
	if before == nil {
		before = map[string]FileFingerprint{}
	}
	if after == nil {
		after = map[string]FileFingerprint{}
	}
	var out []FingerprintChange
	seen := map[string]bool{}
	for path, b := range before {
		seen[path] = true
		a, ok := after[path]
		if !ok || !a.Exists {
			out = append(out, FingerprintChange{Path: path, Content: b.Content})
			continue
		}
		if a.Hash != b.Hash {
			out = append(out, FingerprintChange{Path: path, Content: b.Content})
		}
	}
	for path, a := range after {
		if seen[path] || !a.Exists {
			continue
		}
		out = append(out, FingerprintChange{Path: path, Content: nil})
	}
	return out
}

func shouldSkipFingerprintDir(path string) bool {
	if SkipHiddenPath(path) {
		return true
	}
	switch filepath.Base(path) {
	case ".git", "node_modules", "__pycache__", "vendor", "dist", "build", "target", "bin", "obj":
		return true
	default:
		return false
	}
}

func shouldSkipFingerprintFile(path string, size, maxBytes int64) bool {
	if size < 0 || size > maxBytes {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".dll", ".so", ".dylib", ".a", ".o", ".obj",
		".pyc", ".pyo", ".pyd", ".class", ".jar", ".war",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp",
		".mp3", ".mp4", ".mov", ".avi", ".mkv", ".wav",
		".zip", ".gz", ".bz2", ".xz", ".7z", ".rar", ".tar",
		".pdf", ".woff", ".woff2", ".ttf", ".otf",
		".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

func looksBinary(body []byte) bool {
	n := len(body)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if body[i] == 0 {
			return true
		}
	}
	return false
}
