package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

const (
	v2SessionNotFound    = -32002
	v2UnsupportedVersion = -32022
)

type v2InitializeParams struct {
	Capabilities struct {
		Session struct {
			Commands struct {
				Skill *struct{} `json:"skill"`
			} `json:"commands"`
		} `json:"session"`
	} `json:"capabilities"`
}

type v2SessionResult struct {
	SessionID         string           `json:"sessionId,omitempty"`
	ConfigOptions     []v2ConfigOption `json:"configOptions,omitempty"`
	AvailableCommands []v2Command      `json:"availableCommands"`
}

type v2ConfigOption struct {
	ConfigID     string                     `json:"configId"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description,omitempty"`
	Category     string                     `json:"category"`
	Type         string                     `json:"type"`
	CurrentValue string                     `json:"currentValue"`
	Options      []SessionConfigOptionValue `json:"options"`
}

func v2ConfigOptions(options []SessionConfigOption) []v2ConfigOption {
	result := make([]v2ConfigOption, 0, len(options))
	for _, o := range options {
		result = append(result, v2ConfigOption{
			ConfigID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category,
			Type: o.Type, CurrentValue: o.CurrentValue, Options: o.Options,
		})
	}
	return result
}

func (s *Server) isV2() bool { return s.protocol.Load() == 2 }

func (s *Server) initializeV2(msg jsonrpcMessage) error {
	var params v2InitializeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, "connection already initialized")
	}
	s.v2SkillCommands = params.Capabilities.Session.Commands.Skill != nil
	s.clientCapabilities = ClientCapabilities{}
	s.initialized = true
	s.protocol.Store(2)
	s.mu.Unlock()
	return s.conn.Reply(msg.ID, map[string]any{
		"protocolVersion": 2,
		"info":            ImplementationInfo{Name: "solcode", Title: "solcode", Version: s.version},
		"capabilities": map[string]any{"session": map[string]any{
			"prompt":       map[string]any{"image": struct{}{}, "embeddedContext": struct{}{}, "skill": map[string]any{"inline": struct{}{}}},
			"mcp":          map[string]any{"http": struct{}{}, "stdio": struct{}{}},
			"systemPrompt": SystemPromptCapabilities{Mode: SystemPromptModes{Override: &struct{}{}}},
		}},
	})
}

func (s *Server) v2UnknownSession(msg jsonrpcMessage, id string) error {
	return s.replyErrorData(msg, v2SessionNotFound, "unknown session", map[string]any{"sessionId": id})
}

func (s *Server) replyErrorData(msg jsonrpcMessage, code int, message string, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.conn.Write(jsonrpcMessage{ID: msg.ID, Error: &jsonrpcError{Code: code, Message: message, Data: encoded}})
}

func (s *Server) handleV2Resume(ctx context.Context, msg jsonrpcMessage) error {
	var params struct {
		SessionLoadParams
		ReplayFrom *struct {
			Type string `json:"type"`
		} `json:"replayFrom"`
	}
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.SessionID) == "" || !filepath.IsAbs(params.Cwd) {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "sessionId and absolute cwd are required")
	}
	if params.ReplayFrom != nil && params.ReplayFrom.Type != "start" {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "unsupported replayFrom cursor")
	}
	if err := validateSystemPrompt(params.SystemPrompt); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	mcpServers, err := parseMCPServers(params.MCPServers)
	if err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess, err := s.createSession(ctx, params.SessionID, params.Cwd, mcpServers, params.SystemPrompt)
	if err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	current, err := s.loadPersistedSession(ctx, sess, true)
	if err == nil {
		err = sess.loadV2Prompts()
	}
	if err == nil && params.ReplayFrom != nil {
		s.replayV2History(sess, current)
		err = s.replayPersistedSubagents(ctx, sess)
		if err == nil {
			s.notifyV2(sess.id, map[string]any{"sessionUpdate": "replay_complete"})
		}
	}
	if err != nil {
		sess.close()
		s.removeSession(sess.id)
		if session.IsNotFound(err) {
			return s.v2UnknownSession(msg, sess.id)
		}
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	s.emitV2State(sess.id, "idle", "", nil)
	return s.conn.Reply(msg.ID, v2SessionResult{ConfigOptions: v2ConfigOptions(sess.configOptions()), AvailableCommands: s.v2Commands(sess)})
}

func (s *Server) handleV2List(ctx context.Context, msg jsonrpcMessage) error {
	var params struct {
		Cwd    string `json:"cwd"`
		Cursor string `json:"cursor"`
	}
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	cfg := s.cfg
	if params.Cwd != "" {
		if !filepath.IsAbs(params.Cwd) {
			return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "cwd must be an absolute path")
		}
		applyWorkDir(&cfg, params.Cwd)
	}
	if cfg.Session.Dir == "" {
		cfg.Session.Dir = config.DefaultSessionDir(cfg.WorkDir)
	}
	dirs := []string{cfg.Session.Dir}
	if params.Cwd == "" && cfg.Session.Dir == config.DefaultSessionDir(cfg.WorkDir) {
		projects := filepath.Join(config.UserStateDir(), "projects")
		entries, err := os.ReadDir(projects)
		if err != nil && !os.IsNotExist(err) {
			return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
		dirs = append(dirs, config.DefaultSessionDir(""))
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, filepath.Join(projects, entry.Name(), "sessions"))
			}
		}
	}
	var metas []session.Metadata
	seenDirs := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		if seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true
		entries, err := session.NewFileStore(dir).List(ctx)
		if err != nil {
			return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
		metas = append(metas, entries...)
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	type listedSession struct {
		SessionID session.SessionID `json:"sessionId"`
		Cwd       string            `json:"cwd"`
		Title     string            `json:"title,omitempty"`
		UpdatedAt time.Time         `json:"updatedAt,omitempty"`
	}
	sessions := make([]listedSession, 0, len(metas))
	for _, meta := range metas {
		if !filepath.IsAbs(meta.WorkDir) || (params.Cwd != "" && filepath.Clean(meta.WorkDir) != filepath.Clean(params.Cwd)) {
			continue
		}
		sessions = append(sessions, listedSession{meta.ID, meta.WorkDir, meta.Title, meta.UpdatedAt})
	}
	offset := 0
	if params.Cursor != "" {
		var err error
		offset, err = strconv.Atoi(params.Cursor)
		if err != nil || offset < 0 || offset > len(sessions) {
			return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "invalid session list cursor")
		}
	}
	const pageSize = 100
	end := min(offset+pageSize, len(sessions))
	result := map[string]any{"sessions": sessions[offset:end]}
	if end < len(sessions) {
		result["nextCursor"] = strconv.Itoa(end)
	}
	return s.conn.Reply(msg.ID, result)
}

func (s *Server) handleV2Close(msg jsonrpcMessage) error {
	var params SessionCancelParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess == nil {
		return s.conn.Reply(msg.ID, struct{}{})
	}
	sess.mu.Lock()
	if sess.closing {
		sess.mu.Unlock()
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, "session is already closing")
	}
	done := sess.cancelV2WorkLocked()
	sess.mu.Unlock()
	// Keep reading the connection while cancellation unwinds client calls.
	go func() {
		if done != nil {
			<-done
		}
		sess.close()
		s.removeSession(sess.id)
		_ = s.conn.Reply(msg.ID, struct{}{})
	}()
	return nil
}

func (sess *acpSession) cancelV2Work() <-chan struct{} {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.cancelV2WorkLocked()
}

func (sess *acpSession) cancelV2WorkLocked() <-chan struct{} {
	sess.closing = true
	if sess.cancel != nil {
		sess.cancel()
	}
	return sess.promptDone
}

func (s *Server) finishV2Prompt(sess *acpSession, stop string, err error) {
	if stop == StopReasonCancelled {
		err = nil
	} else if err != nil {
		stop = "error"
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	s.emitV2State(sess.id, "idle", stop, err)
	sess.endPromptLocked()
}

func (s *Server) emitV2State(id, state, stop string, err error) {
	update := map[string]any{"sessionUpdate": "state_update", "state": state}
	if stop != "" {
		update["stopReason"] = stop
	}
	if err != nil {
		update["error"] = jsonrpcError{Code: JSONRPCInternalError, Message: err.Error()}
	}
	s.notifyV2(id, update)
}

func (s *Server) v2Busy(msg jsonrpcMessage) error {
	return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, "session is busy or closing")
}
