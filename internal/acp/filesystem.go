package acp

import (
	"context"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

// clientTextFileSystem delegates text-file access to an ACP client when it
// advertised the matching filesystem capability during initialize.
type clientTextFileSystem struct {
	server       *Server
	sessionID    string
	capabilities FSClientCapabilities
}

var _ tool.TextFileSystem = (*clientTextFileSystem)(nil)

func (s *Server) textFileSystem(sessionID string, capabilities FSClientCapabilities) tool.TextFileSystem {
	if s == nil || s.conn == nil || (!capabilities.ReadTextFile && !capabilities.WriteTextFile) {
		return nil
	}
	return &clientTextFileSystem{server: s, sessionID: sessionID, capabilities: capabilities}
}

func (f *clientTextFileSystem) sessionMode() permission.Mode {
	if f == nil || f.server == nil {
		return permission.ModeAuto
	}
	sess := f.server.getSession(f.sessionID)
	if sess == nil || sess.application == nil || sess.application.Permissions == nil {
		return permission.ModeAuto
	}
	return sess.application.Permissions.Mode()
}

// clientWriteEnabled reports whether writes should go through the client FS.
// Bypass/Goal/Ultra already skip agent-side permission prompts; routing writes through
// fs/write_text_file would still let many clients show a second accept dialog.
// Falling back to local disk keeps these modes prompt-free.
func (f *clientTextFileSystem) clientWriteEnabled() bool {
	switch f.sessionMode() {
	case permission.ModeBypass, permission.ModeGoal, permission.ModeUltra:
		return false
	default:
		return true
	}
}

func (f *clientTextFileSystem) CanReadTextFile() bool {
	return f != nil && f.capabilities.ReadTextFile
}

func (f *clientTextFileSystem) CanWriteTextFile() bool {
	return f != nil && f.capabilities.WriteTextFile && f.clientWriteEnabled()
}

func (f *clientTextFileSystem) ReadTextFile(ctx context.Context, path string) (string, error) {
	if !f.CanReadTextFile() {
		return "", fmt.Errorf("ACP client does not support %s", MethodFSReadTextFile)
	}
	if path = strings.TrimSpace(path); path == "" {
		return "", fmt.Errorf("path is required")
	}
	var result FSReadTextFileResult
	if err := f.server.conn.Call(ctx, MethodFSReadTextFile, FSReadTextFileParams{
		SessionID: f.sessionID,
		Path:      path,
	}, &result); err != nil {
		return "", fmt.Errorf("%s %s: %w", MethodFSReadTextFile, path, err)
	}
	return result.Content, nil
}

func (f *clientTextFileSystem) WriteTextFile(ctx context.Context, path, content string) error {
	if !f.CanWriteTextFile() {
		return fmt.Errorf("ACP client does not support %s", MethodFSWriteTextFile)
	}
	if path = strings.TrimSpace(path); path == "" {
		return fmt.Errorf("path is required")
	}
	var result any
	if err := f.server.conn.Call(ctx, MethodFSWriteTextFile, FSWriteTextFileParams{
		SessionID: f.sessionID,
		Path:      path,
		Content:   content,
	}, &result); err != nil {
		return fmt.Errorf("%s %s: %w", MethodFSWriteTextFile, path, err)
	}
	return nil
}
