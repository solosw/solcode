package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

type AppFactory func(cfg config.Config, opts ...app.Option) (*app.App, config.Config, error)

type PromptFunc func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error)

type StreamEmitter struct {
	Text          func(string)
	Thinking      func(string)
	ToolStart     func(name string, input json.RawMessage, toolUseID string)
	ToolDone      func(name string, output string, isError bool, toolUseID string)
	Usage         func(engine.Usage)
	Status        func(string)
	AgentProgress func(tool.AgentProgressEvent)
}

type Server struct {
	cfg                config.Config
	timeout            time.Duration
	maxTurns           int
	version            string
	newApp             AppFactory
	runPrompt          PromptFunc
	conn               *Conn
	mu                 sync.Mutex
	initialized        bool
	clientCapabilities ClientCapabilities
	sessions           map[string]*acpSession
	sessionSeq         atomic.Uint64
	toolCallSeq        atomic.Uint64
}

type acpSession struct {
	id          string
	diskID      string // persisted session id used by App.RunPromptWithSession
	workDir     string
	cfg         config.Config
	application *app.App
	mu          sync.Mutex
	cancel      context.CancelFunc
	prompting   bool
	toolCalls   map[string]string
	// toolCallsByID maps model tool_use ids (and generated ACP ids) to ACP toolCallIds.
	toolCallsByID map[string]string
	// lastToolInput caches the most recent input per tool name for permission diffs.
	lastToolInput  map[string]json.RawMessage
	fsCapabilities FSClientCapabilities
	// agentToolCalls maps nested subagent activity to ACP toolCallIds.
	agentToolCalls map[string]string
	// agentProgressLines accumulates process lines keyed by subagent toolCallId.
	agentProgressLines map[string][]string
}

func NewServer(cfg config.Config, timeout time.Duration, maxTurns int, version string, newApp AppFactory) *Server {
	if newApp == nil {
		newApp = defaultAppFactory
	}
	if version == "" {
		version = "dev"
	}
	return &Server{
		cfg:      cfg,
		timeout:  timeout,
		maxTurns: maxTurns,
		version:  version,
		newApp:   newApp,
		sessions: make(map[string]*acpSession),
	}
}

func defaultAppFactory(cfg config.Config, opts ...app.Option) (*app.App, config.Config, error) {
	application, err := app.New(cfg, opts...)
	return application, cfg, err
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.conn = NewConn(r, w)
	defer s.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		msg, err := s.conn.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if isResponse(msg) {
			continue
		}
		if len(msg.ID) == 0 {
			if msg.Method == MethodSessionCancel {
				_ = s.handleSessionCancel(msg)
			}
			continue
		}
		if msg.Method == MethodSessionPrompt {
			go func(msg jsonrpcMessage) {
				if err := s.handleSessionPrompt(ctx, msg); err != nil {
					_ = s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
				}
			}(msg)
			continue
		}
		if err := s.handle(ctx, msg); err != nil {
			_ = s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = make(map[string]*acpSession)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.close()
	}
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Server) handle(ctx context.Context, msg jsonrpcMessage) error {
	switch msg.Method {
	case MethodInitialize:
		return s.handleInitialize(msg)
	case MethodAuthenticate:
		return s.conn.Reply(msg.ID, map[string]any{})
	case MethodSessionNew:
		return s.handleSessionNew(ctx, msg)
	case MethodSessionLoad:
		return s.handleSessionLoad(ctx, msg)
	case MethodSessionPrompt:
		return s.handleSessionPrompt(ctx, msg)
	case MethodSessionCancel:
		return s.handleSessionCancel(msg)
	case MethodSessionSetMode:
		return s.handleSessionSetMode(msg)
	default:
		return s.conn.ReplyError(msg.ID, JSONRPCMethodNotFound, "method not found: "+msg.Method)
	}
}

func (s *Server) handleInitialize(msg jsonrpcMessage) error {
	var params InitializeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	s.mu.Lock()
	s.initialized = true
	s.clientCapabilities = params.ClientCapabilities
	s.mu.Unlock()
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		AgentInfo: ImplementationInfo{
			Name:    "solcode",
			Title:   "solcode",
			Version: s.version,
		},
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: PromptCapabilities{
				Image:           true,
				EmbeddedContext: true,
			},
		},
	}
	return s.conn.Reply(msg.ID, result)
}

func (s *Server) handleSessionNew(ctx context.Context, msg jsonrpcMessage) error {
	if err := s.requireInitialized(); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, err.Error())
	}
	var params SessionNewParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	id := s.nextSessionID()
	sess, err := s.createSession(ctx, id, params.Cwd)
	if err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	if err := s.conn.Reply(msg.ID, SessionNewResult{
		SessionID: sess.id,
		Modes:     modeState(sess.application.Permissions.Mode()),
	}); err != nil {
		return err
	}
	// A client generally does not register the session until it receives this
	// response. Publish command completion metadata only afterwards.
	s.emitSessionBootstrap(sess)
	return nil
}

func (s *Server) handleSessionLoad(ctx context.Context, msg jsonrpcMessage) error {
	if err := s.requireInitialized(); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, err.Error())
	}
	var params SessionLoadParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "sessionId is required")
	}
	sess, err := s.createSession(ctx, params.SessionID, params.Cwd)
	if err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	if err := s.replayPersistedHistory(ctx, sess, true); err != nil {
		sess.close()
		s.removeSession(sess.id)
		if session.IsNotFound(err) {
			return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
		}
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	if err := s.conn.Reply(msg.ID, map[string]any{}); err != nil {
		return err
	}
	s.emitSessionBootstrap(sess)
	return nil
}

func (s *Server) handleSessionPrompt(ctx context.Context, msg jsonrpcMessage) error {
	if err := s.requireInitialized(); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, err.Error())
	}
	var params SessionPromptParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess == nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "unknown session")
	}
	prompt := promptToText(params.Prompt, sess.workDir)
	if prompt == "" {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "prompt is required")
	}

	runCtx, cancel := conversationContext(ctx, s.timeout)
	if !sess.beginPrompt(cancel) {
		cancel()
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidRequest, "session is already prompting")
	}
	ended := false
	endPrompt := func() {
		if !ended {
			sess.endPrompt()
			ended = true
		}
	}
	defer endPrompt()

	if handled, stop, err := s.handleSlashCommand(runCtx, sess, prompt); handled {
		if err != nil && stop != StopReasonCancelled {
			endPrompt()
			return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
		if stop == "" {
			stop = StopReasonEndTurn
		}
		endPrompt()
		return s.conn.Reply(msg.ID, SessionPromptResult{StopReason: stop})
	}

	runPrompt := s.runPrompt
	if runPrompt == nil {
		runPrompt = defaultPromptFunc
	}
	result, err := runPrompt(runCtx, sess.application, sess.persistID(), prompt, sess.workDir, s.maxTurns, StreamEmitter{
		Text:      func(text string) { s.emitText(sess, "agent_message_chunk", text) },
		Thinking:  func(text string) { s.emitText(sess, "agent_thought_chunk", text) },
		ToolStart: func(name string, input json.RawMessage, toolUseID string) { s.emitToolStart(sess, name, input, toolUseID) },
		ToolDone:  func(name string, output string, isError bool, toolUseID string) { s.emitToolDone(sess, name, output, isError, toolUseID) },
		Usage: func(usage engine.Usage) {
			s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "usage_update", Usage: usageUpdate(usage)})
		},
		Status: func(status string) {
			s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "status_update", Message: status})
		},
		AgentProgress: func(event tool.AgentProgressEvent) {
			s.emitAgentProgress(sess, event)
		},
	})
	stop := stopReason(runCtx, result, err)
	if err != nil && stop != StopReasonCancelled {
		endPrompt()
		return s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	endPrompt()
	return s.conn.Reply(msg.ID, SessionPromptResult{StopReason: stop})
}

func (s *Server) handleSessionCancel(msg jsonrpcMessage) error {
	var params SessionCancelParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess != nil {
		sess.cancelPrompt()
	}
	if len(msg.ID) == 0 {
		return nil
	}
	return s.conn.Reply(msg.ID, map[string]any{})
}

func (s *Server) handleSessionSetMode(msg jsonrpcMessage) error {
	var params SessionSetModeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess == nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "unknown session")
	}
	next := permission.NormalizeMode(permission.Mode(params.ModeID))
	if next == "" {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "invalid mode")
	}
	if err := applyMode(sess.application, next); err != nil {
		return s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	s.emitUpdate(sess.id, SessionUpdate{
		SessionUpdate: "current_mode_update",
		CurrentModeID: string(next),
	})
	return s.conn.Reply(msg.ID, map[string]any{})
}

func applyMode(application *app.App, next permission.Mode) error {
	if application == nil || application.Permissions == nil {
		return fmt.Errorf("permission service is not configured")
	}
	switch next {
	case permission.ModeAuto, permission.ModeAcceptEdits, permission.ModePlan, permission.ModeBypass, permission.ModeGoal, permission.ModeDefault:
		application.Permissions.SetMode(next)
		application.Config.PermissionMode = next
		application.Config.Permissions.Mode = next
		return nil
	default:
		return fmt.Errorf("unsupported mode %q", next)
	}
}

func defaultPromptFunc(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, _ StreamEmitter) (agent.AgentResult, error) {
	if application == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	return application.RunPromptWithSession(ctx, sessionID, prompt, workDir, maxTurns)
}

func (s *Server) createSession(ctx context.Context, id, cwd string) (*acpSession, error) {
	cfg := s.cfg
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		applyWorkDir(&cfg, cwd)
	}
	if id == "" {
		id = s.nextSessionID()
	}
	cfg.Session.DefaultSession = id
	if cfg.Session.Dir == "" {
		cfg.Session.Dir = config.DefaultSessionDir(cfg.WorkDir)
	}

	s.mu.Lock()
	fsCapabilities := FSClientCapabilities{}
	if s.clientCapabilities.FS != nil {
		fsCapabilities = *s.clientCapabilities.FS
	}
	s.mu.Unlock()
	sess := &acpSession{
		id:                 id,
		diskID:             id,
		workDir:            cfg.WorkDir,
		cfg:                cfg,
		toolCalls:          make(map[string]string),
		toolCallsByID:      make(map[string]string),
		lastToolInput:      make(map[string]json.RawMessage),
		fsCapabilities:     fsCapabilities,
		agentToolCalls:     make(map[string]string),
		agentProgressLines: make(map[string][]string),
	}

	application, cfg, err := s.newApp(cfg,
		app.WithTextFileSystem(s.textFileSystem(id, fsCapabilities)),
		app.WithStreamCallbacks(
			func(text string) { s.emitText(sess, "agent_message_chunk", text) },
			func(text string) { s.emitText(sess, "agent_thought_chunk", text) },
		),
		app.WithToolCallbacks(
			func(name string, input json.RawMessage, toolUseID string) { s.emitToolStart(sess, name, input, toolUseID) },
			func(name string, output string, isError bool, toolUseID string) { s.emitToolDone(sess, name, output, isError, toolUseID) },
		),
		app.WithUsageCallback(func(usage engine.Usage) {
			s.emitUpdate(sess.id, SessionUpdate{
				SessionUpdate: "usage_update",
				Usage:         usageUpdate(usage),
			})
		}),
		app.WithStatusCallback(func(status string) {
			s.emitUpdate(sess.id, SessionUpdate{
				SessionUpdate: "status_update",
				Message:       status,
			})
		}),
		app.WithAgentProgressCallback(func(event tool.AgentProgressEvent) {
			s.emitAgentProgress(sess, event)
		}),
		app.WithAskUserCallback(func(ctx context.Context, params tool.AskUserParams) (map[string]string, error) {
			return s.askUser(ctx, sess, params)
		}),
	)
	if err != nil {
		return nil, err
	}
	sess.cfg = cfg
	sess.application = application
	application.Permissions.SetAskFunc(func(toolName, description string) bool {
		return s.requestPermission(sess, toolName, description)
	})

	s.mu.Lock()
	if existing := s.sessions[id]; existing != nil {
		s.mu.Unlock()
		application.Close()
		return nil, fmt.Errorf("session %q is already open", id)
	}
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess, nil
}

func (s *Server) replayPersistedHistory(ctx context.Context, sess *acpSession, required bool) error {
	if sess == nil || sess.application == nil || sess.application.Sessions == nil {
		if required {
			id := ""
			if sess != nil {
				id = sess.id
			}
			return session.NotFoundError{ID: session.SessionID(id)}
		}
		return nil
	}
	loaded, err := sess.application.Sessions.Load(ctx, session.SessionID(sess.id))
	if err != nil {
		if !required && session.IsNotFound(err) {
			return nil
		}
		return err
	}
	sess.application.SanitizeLoadedSession(loaded)
	for _, update := range sessionHistoryUpdates(loaded) {
		s.emitUpdate(sess.id, update)
	}
	if u := sessionUsageUpdate(ctx, sess.application, loaded, sess.cfg.MaxContextTokens); u != nil {
		s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "usage_update", Usage: u})
	}
	return nil
}

func (s *Server) emitSessionBootstrap(sess *acpSession) {
	if sess == nil {
		return
	}
	s.emitUpdate(sess.id, SessionUpdate{
		SessionUpdate:     "available_commands_update",
		AvailableCommands: availableCommands(),
	})
	if sess.application != nil && sess.application.Permissions != nil {
		s.emitUpdate(sess.id, SessionUpdate{
			SessionUpdate: "current_mode_update",
			CurrentModeID: string(sess.application.Permissions.Mode()),
		})
	}
	// Publish TUI-aligned context occupancy (used=estimate, size=cfg limit).
	s.emitSessionUsage(context.Background(), sess)
}

// emitSessionUsage sends usage_update using the same occupancy numbers as the TUI footer.
func (s *Server) emitSessionUsage(ctx context.Context, sess *acpSession) {
	if s == nil || sess == nil || sess.application == nil {
		return
	}
	var current *session.Session
	if sess.application.Sessions != nil {
		if loaded, err := sess.application.Sessions.Load(ctx, session.SessionID(sess.persistID())); err == nil {
			current = loaded
		}
	}
	maxCtx := int64(0)
	if sess.cfg.MaxContextTokens > 0 {
		maxCtx = sess.cfg.MaxContextTokens
	} else {
		maxCtx = sess.application.Config.MaxContextTokens
	}
	if current == nil {
		// Empty session: still report the window size so clients can render the meter.
		s.emitUpdate(sess.id, SessionUpdate{
			SessionUpdate: "usage_update",
			Usage:         usageUpdateFromSession(0, maxCtx),
		})
		return
	}
	if u := sessionUsageUpdate(ctx, sess.application, current, maxCtx); u != nil {
		s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "usage_update", Usage: u})
	}
}

func (s *Server) emitText(sess *acpSession, kind, text string) {
	if sess == nil || text == "" {
		return
	}
	content := ContentBlock{Type: "text", Text: text}
	s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: kind, Content: &content})
}

func (s *Server) emitToolStart(sess *acpSession, name string, input json.RawMessage, toolUseID string) {
	if sess == nil {
		return
	}
	id := strings.TrimSpace(toolUseID)
	if id == "" {
		id = s.nextToolCallID()
	}
	sess.mu.Lock()
	if sess.toolCallsByID == nil {
		sess.toolCallsByID = make(map[string]string)
	}
	sess.toolCalls[name] = id
	sess.toolCallsByID[id] = id
	if toolUseID != "" && toolUseID != id {
		sess.toolCallsByID[toolUseID] = id
	}
	// Cache last input so permission prompts can attach the same diff preview.
	if sess.lastToolInput == nil {
		sess.lastToolInput = make(map[string]json.RawMessage)
	}
	if len(input) > 0 {
		sess.lastToolInput[name] = append(json.RawMessage(nil), input...)
	}
	workDir := sess.workDir
	sess.mu.Unlock()
	diffs, locations := toolCallDiffs(name, input, workDir)
	s.emitUpdate(sess.id, SessionUpdate{
		SessionUpdate: "tool_call",
		ToolCallID:    id,
		Title:         name,
		Kind:          toolKind(name),
		Status:        ToolCallInProgress,
		RawInput:      input,
		ToolContent:   diffs,
		Locations:     locations,
	})
}

func (s *Server) emitToolDone(sess *acpSession, name, output string, isError bool, toolUseID string) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	id := strings.TrimSpace(toolUseID)
	if id == "" {
		id = sess.toolCalls[name]
	} else if mapped := sess.toolCallsByID[id]; mapped != "" {
		id = mapped
	}
	input := append(json.RawMessage(nil), sess.lastToolInput[name]...)
	delete(sess.toolCalls, name)
	delete(sess.lastToolInput, name)
	delete(sess.toolCallsByID, id)
	if toolUseID != "" {
		delete(sess.toolCallsByID, toolUseID)
	}
	sess.mu.Unlock()
	if id == "" {
		id = s.nextToolCallID()
	}
	status := ToolCallCompleted
	if isError {
		status = ToolCallFailed
	}
	raw, _ := json.Marshal(map[string]string{"output": output})
	toolContent, locations := toolOutputContent(name, output, isError)
	s.emitUpdate(sess.id, SessionUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    id,
		Title:         name,
		Kind:          toolKind(name),
		Status:        status,
		RawOutput:     raw,
		ToolContent:   toolContent,
		Locations:     locations,
	})
	if name == tool.TodoWriteToolName && !isError {
		s.emitPlanUpdate(sess, input)
	}
}

func (s *Server) emitAgentProgress(sess *acpSession, event tool.AgentProgressEvent) {
	if sess == nil {
		return
	}
	agentKey := strings.TrimSpace(event.AgentID)
	if agentKey == "" {
		agentKey = strings.TrimSpace(event.TaskID)
	}
	if agentKey == "" {
		agentKey = "subagent"
	}
	label := strings.TrimSpace(event.Description)
	if label == "" {
		label = strings.TrimSpace(event.TaskID)
	}
	if label == "" {
		label = agentKey
	}
	title := label
	line := ""
	status := ToolCallInProgress
	switch strings.ToLower(strings.TrimSpace(event.Kind)) {
	case "started":
		line = "started"
	case "retry":
		line = strings.TrimSpace(event.Output)
		if line == "" {
			line = "retrying"
		}
	case "tool_start":
		line = "→ " + event.ToolName
		if event.ToolName != "" {
			title = label + " · " + event.ToolName
		}
	case "tool_done":
		if event.IsError {
			line = event.ToolName + " failed"
		} else {
			line = event.ToolName + " done"
		}
		if summary := oneLine(event.Output); summary != "" {
			line += ": " + truncateRunes(summary, 120)
		}
	case "completed", "failed", "cancelled", "canceled":
		line = event.Kind
		if summary := oneLine(event.Output); summary != "" {
			line += ": " + truncateRunes(summary, 160)
		}
		status = ToolCallCompleted
		if event.IsError || event.Kind == "failed" {
			status = ToolCallFailed
		}
	default:
		line = strings.TrimSpace(event.Output)
		if line == "" {
			line = event.Kind
		}
	}

	id := s.ensureAgentToolCall(sess, agentKey, title, ToolCallInProgress, nil)
	sess.mu.Lock()
	if sess.agentProgressLines == nil {
		sess.agentProgressLines = make(map[string][]string)
	}
	lines := append(append([]string{}, sess.agentProgressLines[id]...), line)
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	sess.agentProgressLines[id] = lines
	body := strings.Join(lines, "\n")
	if status == ToolCallCompleted || status == ToolCallFailed {
		delete(sess.agentToolCalls, agentKey)
		delete(sess.agentProgressLines, id)
	}
	sess.mu.Unlock()

	raw, _ := json.Marshal(map[string]string{"output": body})
	s.emitUpdate(sess.id, SessionUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    id,
		Title:         title,
		Kind:          "think",
		Status:        status,
		RawOutput:     raw,
		ToolContent:   []ToolCallContent{{Type: "content", Content: &ContentBlock{Type: "text", Text: body}}},
	})
}

func (s *Server) ensureAgentToolCall(sess *acpSession, agentKey, title, status string, input json.RawMessage) string {
	sess.mu.Lock()
	if sess.agentToolCalls == nil {
		sess.agentToolCalls = make(map[string]string)
	}
	id := sess.agentToolCalls[agentKey]
	if id == "" {
		id = s.nextToolCallID()
		sess.agentToolCalls[agentKey] = id
		sess.mu.Unlock()
		s.emitUpdate(sess.id, SessionUpdate{
			SessionUpdate: "tool_call",
			ToolCallID:    id,
			Title:         title,
			Kind:          "think",
			Status:        status,
			RawInput:      input,
		})
		return id
	}
	sess.mu.Unlock()
	return id
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func (s *Server) emitPlanUpdate(sess *acpSession, input json.RawMessage) {
	if sess == nil || len(input) == 0 {
		return
	}
	var params tool.TodoWriteParams
	if err := json.Unmarshal(input, &params); err != nil {
		return
	}
	entries := make([]PlanEntry, 0, len(params.Todos))
	allCompleted := len(params.Todos) > 0
	for _, todo := range params.Todos {
		entries = append(entries, PlanEntry{
			Content:  todo.Content,
			Priority: todo.Priority,
			Status:   todo.Status,
		})
		if todo.Status != "completed" {
			allCompleted = false
		}
	}
	// TodoWrite clears its persisted list once every item is completed; mirror
	// that final state so clients replace their displayed plan with an empty one.
	if allCompleted {
		entries = []PlanEntry{}
	}
	s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "plan", PlanEntries: entries})
}

func (s *Server) requestPermission(sess *acpSession, toolName, description string) bool {
	if sess == nil || s.conn == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	sess.mu.Lock()
	id := sess.toolCalls[toolName]
	input := sess.lastToolInput[toolName]
	workDir := sess.workDir
	sess.mu.Unlock()
	if id == "" {
		id = s.nextToolCallID()
	}

	content := []ToolCallContent{{
		Type:    "content",
		Content: &ContentBlock{Type: "text", Text: description},
	}}
	var locations []ToolCallLocation
	if diffs, locs := toolCallDiffs(toolName, input, workDir); len(diffs) > 0 || len(locs) > 0 {
		content = append(content, diffs...)
		locations = locs
	}

	var result RequestPermissionResult
	err := s.conn.Call(ctx, MethodSessionRequestPermission, RequestPermissionParams{
		SessionID: sess.id,
		ToolCall: ToolCallUpdate{
			ToolCallID: id,
			Title:      toolName,
			Kind:       toolKind(toolName),
			Status:     ToolCallPending,
			RawInput:   input,
			Content:    content,
			Locations:  locations,
		},
		Options: defaultPermissionOptions(),
	}, &result)
	if err != nil {
		log.Printf("acp permission request failed: %v", err)
		return false
	}
	switch result.Outcome.OptionID {
	case PermissionAllowAlways:
		if sess.application != nil && sess.application.Permissions != nil {
			sess.application.Permissions.AllowTool(toolName)
		}
		return true
	case PermissionAllowOnce:
		return true
	case PermissionRejectAlways:
		if sess.application != nil && sess.application.Permissions != nil {
			sess.application.Permissions.DenyTool(toolName)
		}
		return false
	default:
		return false
	}
}

func (s *Server) askUser(ctx context.Context, sess *acpSession, params tool.AskUserParams) (map[string]string, error) {
	answers := make(map[string]string, len(params.Questions))
	for i, question := range params.Questions {
		options := make([]PermissionOption, 0, len(question.Options)+1)
		for _, option := range question.Options {
			options = append(options, PermissionOption{
				OptionID: option.Label,
				Name:     option.Label,
				Kind:     "allow_once",
			})
		}
		if len(options) == 0 {
			options = defaultPermissionOptions()
		}
		header := strings.TrimSpace(question.Header)
		if header == "" {
			header = fmt.Sprintf("question-%d", i+1)
		}
		var result RequestPermissionResult
		err := s.conn.Call(ctx, MethodSessionRequestPermission, RequestPermissionParams{
			SessionID: sess.id,
			ToolCall: ToolCallUpdate{
				ToolCallID: s.nextToolCallID(),
				Title:      header,
				Kind:       "other",
				Status:     ToolCallPending,
				Content: []ToolCallContent{{
					Type:    "content",
					Content: &ContentBlock{Type: "text", Text: question.Question},
				}},
			},
			Options: options,
		}, &result)
		if err != nil {
			return nil, err
		}
		if result.Outcome.OptionID == "" || result.Outcome.OptionID == PermissionRejectOnce || result.Outcome.OptionID == PermissionRejectAlways {
			return nil, fmt.Errorf("user cancelled AskUser")
		}
		answers[header] = result.Outcome.OptionID
	}
	return answers, nil
}

func (s *Server) emitUpdate(sessionID string, update SessionUpdate) {
	if s.conn == nil || sessionID == "" {
		return
	}
	if err := s.conn.Notify(MethodSessionUpdate, SessionUpdateParams{
		SessionID: sessionID,
		Update:    update,
	}); err != nil {
		log.Printf("acp session update failed: %v", err)
	}
}

func (s *Server) getSession(id string) *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) removeSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Server) requireInitialized() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		return fmt.Errorf("initialize must be called first")
	}
	return nil
}

func (s *Server) nextSessionID() string {
	n := s.sessionSeq.Add(1)
	// ACP clients often restart the agent process for each editor window. Include
	// a timestamp so a new session can never collide with persisted history from
	// an earlier ACP process.
	return fmt.Sprintf("acp-%d-%d", time.Now().UTC().UnixNano(), n)
}

func (s *Server) nextToolCallID() string {
	n := s.toolCallSeq.Add(1)
	return fmt.Sprintf("tool-%d", n)
}

func (sess *acpSession) beginPrompt(cancel context.CancelFunc) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.prompting {
		return false
	}
	sess.prompting = true
	sess.cancel = cancel
	return true
}

func (sess *acpSession) endPrompt() {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.prompting = false
	if sess.cancel != nil {
		sess.cancel()
		sess.cancel = nil
	}
}

func (sess *acpSession) cancelPrompt() {
	sess.mu.Lock()
	cancel := sess.cancel
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (sess *acpSession) close() {
	sess.cancelPrompt()
	if sess.application != nil {
		_ = sess.application.Close()
	}
}

func conversationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func applyWorkDir(cfg *config.Config, workDir string) {
	if cfg == nil || workDir == "" {
		return
	}
	previous := cfg.WorkDir
	cfg.WorkDir = workDir
	if cfg.Session.Dir == "" || cfg.Session.Dir == config.DefaultSessionDir(previous) {
		cfg.Session.Dir = config.DefaultSessionDir(cfg.WorkDir)
	}
	if cfg.Memory.Dir == "" || cfg.Memory.Dir == config.DefaultMemoryDir(previous) {
		cfg.Memory.Dir = config.DefaultMemoryDir(cfg.WorkDir)
	}
}

func stopReason(ctx context.Context, result agent.AgentResult, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || (err != nil && errors.Is(err, context.Canceled)) || result.Error == context.Canceled.Error() {
		return StopReasonCancelled
	}
	if err != nil {
		return StopReasonRefusal
	}
	if result.Error != "" {
		if strings.Contains(result.Error, "max turns reached") {
			return StopReasonMaxTurn
		}
		if result.Error == context.Canceled.Error() {
			return StopReasonCancelled
		}
		return StopReasonRefusal
	}
	return StopReasonEndTurn
}

func decodeParams(raw json.RawMessage, dest any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dest)
}
