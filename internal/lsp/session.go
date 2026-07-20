package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// session is one language-server process bound to a workspace root.
type session struct {
	cmd      *exec.Cmd
	conn     *conn
	language string
	root     string
	stdin    io.WriteCloser
	stdout   io.ReadCloser

	mu           sync.Mutex
	openDocs     map[string]int // uri -> version
	initialized  bool
	capabilities map[string]any
}

func startSession(ctx context.Context, command ServerCommand, workDir string) (*session, error) {
	if len(command.Command) == 0 {
		return nil, fmt.Errorf("%w: empty command for language %s", ErrServerUnavailable, command.Language)
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workDir = wd
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}

	name := command.Command[0]
	args := append([]string(nil), command.Command[1:]...)
	// Process lifetime is independent of individual request contexts.
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	// Suppress noisy stderr from language servers.
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("%w: start %s: %v", ErrServerUnavailable, name, err)
	}

	s := &session{
		cmd:      cmd,
		conn:     newConn(stdout, stdin),
		language: command.Language,
		root:     workDir,
		stdin:    stdin,
		stdout:   stdout,
		openDocs: make(map[string]int),
	}
	if err := s.initialize(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *session) initialize(ctx context.Context) error {
	rootURI := pathToURI(s.root)
	params := map[string]any{
		"processId": os.Getpid(),
		"clientInfo": map[string]any{
			"name":    "solcode",
			"version": "0.1",
		},
		"rootUri": rootURI,
		"rootPath": s.root,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"definition":     map[string]any{"linkSupport": true},
				"references":     map[string]any{},
				"implementation": map[string]any{"linkSupport": true},
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
				},
				"synchronization": map[string]any{
					"didSave":   true,
					"willSave":  false,
					"dynamicRegistration": false,
				},
			},
			"workspace": map[string]any{
				"symbol": map[string]any{},
				"workspaceFolders": true,
			},
		},
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": filepath.Base(s.root)},
		},
		"trace": "off",
	}
	result, err := s.conn.Call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var initResult struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if len(result) > 0 {
		_ = jsonUnmarshal(result, &initResult)
		s.capabilities = initResult.Capabilities
	}
	if err := s.conn.Notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized notify: %w", err)
	}
	s.initialized = true
	return nil
}

func (s *session) ensureOpen(ctx context.Context, filePath string) (string, error) {
	if abs, err := filepath.Abs(filePath); err == nil {
		filePath = abs
	}
	uri := pathToURI(filePath)

	s.mu.Lock()
	if _, ok := s.openDocs[uri]; ok {
		s.mu.Unlock()
		return uri, nil
	}
	s.mu.Unlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file for didOpen: %w", err)
	}
	langID := languageIDForPath(filePath, s.language)
	version := 1
	params := map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": langID,
			"version":    version,
			"text":       string(data),
		},
	}
	if err := s.conn.Notify("textDocument/didOpen", params); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.openDocs[uri] = version
	s.mu.Unlock()
	// Some servers need a tick after didOpen before answering queries.
	_ = ctx
	return uri, nil
}

func (s *session) Close() {
	if s == nil {
		return
	}
	if s.conn != nil {
		// Best-effort shutdown.
		ctx := context.Background()
		_, _ = s.conn.Call(ctx, "shutdown", nil)
		_ = s.conn.Notify("exit", nil)
		s.conn.Close()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.stdout != nil {
		_ = s.stdout.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
}

func languageIDForPath(path, fallback string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".h":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".hpp", ".hxx":
		return "cpp"
	case ".java":
		return "java"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	}
	if fallback != "" {
		return fallback
	}
	return "plaintext"
}

// jsonUnmarshal is a thin alias to avoid importing encoding/json in every call site clutter.
func jsonUnmarshal(data []byte, v any) error {
	return decodeJSON(data, v)
}
