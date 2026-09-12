package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/httpproxy"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/skill"
)

type slashCommand struct {
	Name string
	Args string
}

func parseSlashCommand(input string) (slashCommand, bool) {
	input = strings.TrimSpace(input)
	// Only a single leading slash introduces a command. "//..." and bare "/"
	// are ordinary chat (e.g. comments, paths, or incomplete typing).
	if !strings.HasPrefix(input, "/") || strings.HasPrefix(input, "//") || input == "/" {
		return slashCommand{}, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if body == "" {
		return slashCommand{}, false
	}
	name, args, _ := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	args = strings.TrimSpace(args)
	if !isSlashCommandName(name) {
		return slashCommand{}, false
	}
	return slashCommand{Name: name, Args: args}, true
}

// isSlashCommandName reports whether name is a plausible /command token.
// Unknown-but-well-formed names may still fall through to chat later.
func isSlashCommandName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			// ok
		case r == '-' || r == '_' || r == '.':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func slashHelpText() string {
	return strings.Join([]string{
		"Available commands:",
		"/help — show this help",
		"/status — show model, context, and cache usage",
		"/model — select a model from the current provider",
		"/provider — select a provider",
		"/effort — select thinking effort",
		"/sessions — list or switch saved sessions",
		"/compact — compact the current session now",
		"/checkpoints — list code checkpoints for the current session",
		"/checkpoint-name <name> [turn] — label a checkpoint (default: newest)",
		"/rewind <turn|name> — restore workspace files to a previous turn (code only)",
		"/fix-session — repair invalid tool-use chains in the current session",
		"/new-session [name] — create and switch to a new session",
		"/skills — browse skills and toggle enabled/disabled",
		"/mcp — browse MCP servers and toggle enabled/disabled",
		"/proxy [url|on|off|clear] — show or set HTTP(S) proxy",
		"/goal [description] — work from goal.md until complete",
		"/workflows — list loaded workflows",
	}, "\n")
}

// handleSlashCommand runs local ACP slash commands. ok=false means the prompt
// should fall through to the normal agent loop (for example skill names).
func (s *Server) handleSlashCommand(ctx context.Context, sess *acpSession, input string) (handled bool, stop string, err error) {
	cmd, ok := parseSlashCommand(input)
	if !ok {
		return false, "", nil
	}
	if sess == nil || sess.application == nil {
		return true, StopReasonEndTurn, fmt.Errorf("session is not ready")
	}

	// Skills fall through to the agent loop (Skill tool). Anything else that
	// isn't a builtin is ordinary chat — do not intercept with "Unknown command".
	if !isBuiltinSlashCommand(cmd.Name) {
		return false, "", nil
	}

	persistencePath := config.PersistencePath("", sess.workDir)
	switch cmd.Name {
	case "help":
		s.emitText(sess, "agent_message_chunk", slashHelpText())
		return true, StopReasonEndTurn, nil
	case "status":
		msg, err := s.slashStatus(ctx, sess)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "clear":
		s.emitText(sess, "agent_message_chunk", "Transcript clear is handled by the client UI in ACP mode.")
		return true, StopReasonEndTurn, nil
	case "model":
		msg, err := s.slashPickModel(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "provider":
		msg, err := s.slashPickProvider(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "effort":
		msg, err := s.slashPickEffort(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "sessions":
		msg, err := s.slashSessions(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "skills":
		msg, err := s.slashSkills(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "mcp":
		msg, err := s.slashMCP(ctx, sess, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "proxy":
		msg := s.slashProxy(sess, cmd.Args, persistencePath)
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "compact":
		msg, err := s.slashCompact(ctx, sess)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "checkpoints":
		s.emitText(sess, "agent_message_chunk", app.FormatCheckpointsList(sess.application, sess.persistID(), sess.workDir))
		return true, StopReasonEndTurn, nil
	case "checkpoint-name":
		s.emitText(sess, "agent_message_chunk", app.FormatCheckpointNameCommand(sess.application, sess.persistID(), sess.workDir, cmd.Args))
		return true, StopReasonEndTurn, nil
	case "rewind":
		s.emitText(sess, "agent_message_chunk", app.FormatRewindCommand(sess.application, sess.persistID(), sess.workDir, cmd.Args))
		return true, StopReasonEndTurn, nil
	case "fix-session":
		msg, err := s.slashFixSession(ctx, sess)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "new-session":
		msg, err := s.slashNewSession(ctx, sess, cmd.Args, persistencePath)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		s.emitText(sess, "agent_message_chunk", msg)
		return true, StopReasonEndTurn, nil
	case "goal":
		// Fall through to agent loop after rewriting into a goal run via App API.
		result, err := sess.application.RunGoalWithSession(ctx, sess.persistID(), cmd.Args, sess.workDir, s.maxTurns)
		if err != nil {
			return true, stopReasonFromErr(ctx, err), errIfNotCancel(ctx, err)
		}
		if text := strings.TrimSpace(result.Output); text != "" {
			s.emitText(sess, "agent_message_chunk", text)
		} else if result.Error != "" {
			s.emitText(sess, "agent_message_chunk", result.Error)
		} else {
			s.emitText(sess, "agent_message_chunk", "Goal run finished.")
		}
		return true, stopReasonFromResult(ctx, result.Error), nil
	case "workflows":
		s.emitText(sess, "agent_message_chunk", slashWorkflowsText(sess))
		return true, StopReasonEndTurn, nil
	case "workflow", "workflow-edit", "web-ui":
		s.emitText(sess, "agent_message_chunk", fmt.Sprintf("/%s is only available in the TUI for now.", cmd.Name))
		return true, StopReasonEndTurn, nil
	default:
		// Builtins are listed exhaustively above; treat gaps as chat, not errors.
		return false, "", nil
	}
}

func isBuiltinSlashCommand(name string) bool {
	switch name {
	case "help", "status", "clear", "model", "provider", "effort", "sessions", "compact",
		"checkpoints", "checkpoint-name", "rewind",
		"fix-session", "new-session", "skills", "mcp", "proxy", "goal", "workflows",
		"workflow", "workflow-edit", "web-ui":
		return true
	default:
		return false
	}
}

func (sess *acpSession) persistID() string {
	if sess == nil {
		return ""
	}
	if id := strings.TrimSpace(sess.diskID); id != "" {
		return id
	}
	return sess.id
}

func (s *Server) slashPickModel(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	models := sess.cfg.ListModels()
	if len(models) == 0 {
		return "No models available for the current provider.", nil
	}
	options := make([]PermissionOption, 0, len(models))
	for _, m := range models {
		label := m.Name
		if m.DisplayName != "" {
			label = m.DisplayName
		}
		if m.Current {
			label += " (current)"
		}
		options = append(options, PermissionOption{OptionID: m.Name, Name: label, Kind: "allow_once"})
	}
	choice, err := s.requestChoice(ctx, sess, "model", "Select a model", options)
	if err != nil {
		return "", err
	}
	next, err := sess.cfg.WithModel(choice)
	if err != nil {
		return fmt.Sprintf("Could not switch model to %q: %v", choice, err), nil
	}
	if err := sess.application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch model to %q: %v", choice, err), nil
	}
	sess.cfg = next
	msg := fmt.Sprintf("Switched model to %s. Future prompts will use this model.", next.Model)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"model": choice}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist model selection: %v", err)
	}
	return msg, nil
}

func (s *Server) slashPickProvider(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	if len(sess.cfg.Providers) == 0 {
		return "No providers configured.", nil
	}
	options := make([]PermissionOption, 0, len(sess.cfg.Providers))
	for _, p := range sess.cfg.Providers {
		label := p.Name
		if sess.cfg.Provider == p.Name {
			label += " (current)"
		}
		options = append(options, PermissionOption{OptionID: p.Name, Name: label, Kind: "allow_once"})
	}
	choice, err := s.requestChoice(ctx, sess, "provider", "Select a provider", options)
	if err != nil {
		return "", err
	}
	next, err := sess.cfg.WithProvider(choice)
	if err != nil {
		return fmt.Sprintf("Could not switch provider to %q: %v", choice, err), nil
	}
	if err := sess.application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch provider to %q: %v", choice, err), nil
	}
	sess.cfg = next
	msg := fmt.Sprintf("Switched provider to %s. Model is now %s.", next.Provider, next.Model)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"provider": choice}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist provider selection: %v", err)
	}
	return msg, nil
}

func (s *Server) slashPickEffort(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	options := make([]PermissionOption, 0, len(efforts))
	for _, effort := range efforts {
		label := effort
		if sess.cfg.Effort == effort {
			label += " (current)"
		}
		options = append(options, PermissionOption{OptionID: effort, Name: label, Kind: "allow_once"})
	}
	choice, err := s.requestChoice(ctx, sess, "effort", "Select thinking effort", options)
	if err != nil {
		return "", err
	}
	next := sess.cfg
	next.Effort = choice
	if err := sess.application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch effort to %q: %v", choice, err), nil
	}
	sess.cfg = next
	msg := fmt.Sprintf("Switched effort to %s.", choice)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"effort": choice}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist effort selection: %v", err)
	}
	return msg, nil
}

func (s *Server) slashSessions(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	if sess.application.Sessions == nil {
		return "Sessions are not enabled.", nil
	}
	metas, err := sess.application.Sessions.List(ctx)
	if err != nil {
		return fmt.Sprintf("Could not list sessions: %v", err), nil
	}
	current := sess.persistID()
	if len(metas) == 0 {
		return fmt.Sprintf("No saved sessions yet. Current session: %s\nUse /new-session [name] to create one.", current), nil
	}
	options := make([]PermissionOption, 0, len(metas))
	for _, meta := range metas {
		label := string(meta.ID)
		if string(meta.ID) == current {
			label += " (current)"
		}
		if meta.Model != "" {
			label += " · " + meta.Model
		}
		options = append(options, PermissionOption{OptionID: string(meta.ID), Name: label, Kind: "allow_once"})
	}
	choice, err := s.requestChoice(ctx, sess, "sessions", "Select a session to switch to", options)
	if err != nil {
		return "", err
	}
	return s.switchDiskSession(ctx, sess, choice, persistencePath)
}

func (s *Server) slashSkills(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	registry := skill.LoadFromDirs(sess.cfg.Skills.Paths...)
	defs := registry.All()
	if len(defs) == 0 {
		return "No skills found in configured skill directories.", nil
	}
	options := make([]PermissionOption, 0, len(defs))
	for _, def := range defs {
		enabled := false
		if sess.application.SkillRegistry != nil {
			_, enabled = sess.application.SkillRegistry.Find(def.Name)
		}
		status := "disabled"
		if enabled {
			status = "enabled"
		}
		options = append(options, PermissionOption{
			OptionID: def.Name,
			Name:     fmt.Sprintf("%s (%s)", def.Name, status),
			Kind:     "allow_once",
		})
	}
	choice, err := s.requestChoice(ctx, sess, "skills", "Select a skill to toggle", options)
	if err != nil {
		return "", err
	}
	return s.toggleSkill(sess, choice, persistencePath)
}

func (s *Server) slashMCP(ctx context.Context, sess *acpSession, persistencePath string) (string, error) {
	if len(sess.cfg.MCP.Servers) == 0 {
		return "No MCP servers configured.", nil
	}
	options := make([]PermissionOption, 0, len(sess.cfg.MCP.Servers))
	for _, server := range sess.cfg.MCP.Servers {
		status := "enabled"
		if server.Disabled {
			status = "disabled"
		}
		options = append(options, PermissionOption{
			OptionID: server.Name,
			Name:     fmt.Sprintf("%s (%s)", server.Name, status),
			Kind:     "allow_once",
		})
	}
	choice, err := s.requestChoice(ctx, sess, "mcp", "Select an MCP server to toggle", options)
	if err != nil {
		return "", err
	}
	return s.toggleMCP(sess, choice, persistencePath)
}

func (s *Server) slashCompact(ctx context.Context, sess *acpSession) (string, error) {
	if sess.application.Sessions == nil {
		return "Sessions are not enabled.", nil
	}
	_, changed, err := sess.application.CompactSession(ctx, sess.persistID(), sess.workDir)
	if err != nil {
		return fmt.Sprintf("Compact failed: %v", err), nil
	}
	if !changed {
		return "Compact skipped: current session is below the compaction threshold.", nil
	}
	return "Compacted current session.", nil
}

func (s *Server) slashStatus(ctx context.Context, sess *acpSession) (string, error) {
	if sess == nil || sess.application == nil {
		return "Session is not ready.", nil
	}

	modelName := strings.TrimSpace(sess.cfg.Model)
	if modelName == "" {
		modelName = "solcode"
	}
	provider := strings.TrimSpace(sess.cfg.Provider)
	effort := strings.TrimSpace(sess.cfg.Effort)
	sessionName := strings.TrimSpace(sess.persistID())
	workDir := strings.TrimSpace(sess.workDir)

	var (
		estimated int64
		input     int64
		output    int64
		cacheW    int64
		cacheR    int64
	)
	maxCtx := sess.cfg.MaxContextTokens
	if sess.application.Sessions != nil && sessionName != "" {
		if loaded, err := sess.application.Sessions.Load(ctx, session.SessionID(sessionName)); err == nil && loaded != nil {
			estimated = sess.application.EstimateSessionContextTokens(ctx, loaded)
			input = loaded.Metadata.Usage.InputTokens
			output = loaded.Metadata.Usage.OutputTokens
			cacheW = loaded.Metadata.Usage.CacheCreationInputTokens
			cacheR = loaded.Metadata.Usage.CacheReadInputTokens
		}
	}

	inputSide := statusInputSideTotal(input, cacheR, cacheW)
	cacheUsed := maxInt64(0, cacheR) + maxInt64(0, cacheW)

	lines := []string{
		fmt.Sprintf("Model: %s", modelName),
	}
	if provider != "" {
		lines = append(lines, fmt.Sprintf("Provider: %s", provider))
	}
	if effort != "" {
		lines = append(lines, fmt.Sprintf("Effort: %s", effort))
	}
	if sessionName != "" {
		lines = append(lines, fmt.Sprintf("Session: %s", sessionName))
	}
	if workDir != "" {
		lines = append(lines, fmt.Sprintf("Workdir: %s", workDir))
	}
	lines = append(lines, proxyStatusLine())
	lines = append(lines,
		fmt.Sprintf("Context: %s / %s (%s)", compactStatusTokens(estimated), renderStatusLimit(maxCtx), statusSharePercent(estimated, maxCtx)),
		fmt.Sprintf("Cache: %s / %s input-side (%s) — read %s, write %s",
			compactStatusTokens(cacheUsed),
			compactStatusTokens(inputSide),
			statusSharePercent(cacheUsed, inputSide),
			compactStatusTokens(maxInt64(0, cacheR)),
			compactStatusTokens(maxInt64(0, cacheW)),
		),
		fmt.Sprintf("Output: %s (session total)", compactStatusTokens(maxInt64(0, output))),
		fmt.Sprintf("Input: %s uncached (session total)", compactStatusTokens(maxInt64(0, input))),
	)
	return strings.Join(lines, "\n"), nil
}

func proxyStatusLine() string {
	// Live process state so /proxy takes effect immediately in /status.
	return httpproxy.StatusLine(httpproxy.Get())
}

func (s *Server) slashProxy(sess *acpSession, args, persistencePath string) string {
	cfg, message, err := httpproxy.ApplyCommand(args)
	if err != nil {
		return fmt.Sprintf("Could not update proxy: %v", err)
	}
	if sess != nil {
		sess.cfg.Proxy = cfg
		if sess.application != nil {
			sess.application.Config.Proxy = cfg
		}
	}
	if strings.TrimSpace(args) != "" {
		if err := config.SaveLocalOverrides(persistencePath, httpproxy.PersistMap(cfg)); err != nil {
			message += fmt.Sprintf("\nWarning: could not persist proxy settings: %v", err)
		}
	}
	return message
}

func statusInputSideTotal(inputTokens, cacheRead, cacheWrite int64) int64 {
	total := int64(0)
	if inputTokens > 0 {
		total += inputTokens
	}
	if cacheRead > 0 {
		total += cacheRead
	}
	if cacheWrite > 0 {
		total += cacheWrite
	}
	return total
}

func statusSharePercent(part, total int64) string {
	if part < 0 {
		part = 0
	}
	if total <= 0 {
		return "0%"
	}
	percent := part * 100 / total
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%d%%", percent)
}

func compactStatusTokens(value int64) string {
	if value >= 1_000_000 {
		if value%1_000_000 == 0 {
			return fmt.Sprintf("%dM", value/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		if value%1_000 == 0 {
			return fmt.Sprintf("%dk", value/1_000)
		}
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	}
	return fmt.Sprintf("%d", value)
}

func renderStatusLimit(value int64) string {
	if value <= 0 {
		return "?"
	}
	return compactStatusTokens(value)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s *Server) slashFixSession(ctx context.Context, sess *acpSession) (string, error) {
	if sess.application.Sessions == nil {
		return "Sessions are not enabled.", nil
	}
	_, removed, err := sess.application.RepairSession(ctx, sess.persistID(), sess.workDir)
	if err != nil {
		return fmt.Sprintf("Session repair failed: %v", err), nil
	}
	if removed == 0 {
		return "Session already valid; no incomplete tool exchanges found.", nil
	}
	return fmt.Sprintf("Repaired current session: removed %d incomplete tool block(s).", removed), nil
}

func (s *Server) slashNewSession(ctx context.Context, sess *acpSession, args, persistencePath string) (string, error) {
	if sess.application.Sessions == nil {
		return "Sessions are not enabled.", nil
	}
	name := strings.TrimSpace(args)
	if name == "" {
		name = "session-" + time.Now().Format("20060102-150405")
	}
	name = sanitizeSessionName(name)
	if name == "" {
		return "Session name is required.", nil
	}
	cross, err := s.requestChoice(ctx, sess, "new-session",
		fmt.Sprintf("Enable cross-session memory for %q?", name),
		[]PermissionOption{
			{OptionID: "yes", Name: "Yes — share memories", Kind: "allow_once"},
			{OptionID: "no", Name: "No — keep isolated", Kind: "allow_once"},
		})
	if err != nil {
		return "", err
	}
	crossSessionMemory := cross == "yes"

	loaded, _, err := sess.application.Sessions.LoadOrCreateAndAcquireIgnoringExisting(ctx, session.SessionID(name), sess.workDir, sess.cfg.Model)
	if err != nil {
		return fmt.Sprintf("Could not open session %q: %v", name, err), nil
	}
	if loaded == nil {
		loaded = session.NewSession(session.SessionID(name), sess.workDir, sess.cfg.Model)
	}
	if sess.application.SanitizeLoadedSession(loaded) {
		if err := sess.application.Sessions.Save(ctx, loaded); err != nil {
			_ = sess.application.Sessions.Release(session.SessionID(name))
			return fmt.Sprintf("Could not load session %q: %v", name, err), nil
		}
	}
	loaded.Metadata.Title = name
	loaded.Metadata.CrossSessionMemory = &crossSessionMemory
	loaded.Metadata.MemoryBootstrapPending = crossSessionMemory
	if err := sess.application.Sessions.Save(ctx, loaded); err != nil {
		_ = sess.application.Sessions.Release(session.SessionID(name))
		return fmt.Sprintf("Could not save session %q: %v", name, err), nil
	}
	if old := sess.persistID(); old != "" && old != name {
		_ = sess.application.Sessions.Release(session.SessionID(old))
	}
	sess.diskID = name
	sess.cfg.Session.DefaultSession = name
	sess.application.Config.Session.DefaultSession = name
	for _, update := range sessionHistoryUpdates(loaded) {
		s.emitUpdate(sess.id, update)
	}
	if u := sessionUsageUpdate(ctx, sess.application, loaded, sess.cfg.MaxContextTokens); u != nil {
		s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "usage_update", Usage: u})
	}
	msg := fmt.Sprintf("Started new session: %s (cross-session memory: %v)", name, crossSessionMemory)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"session": map[string]any{"default_session": name}}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist default session: %v", err)
	}
	return msg, nil
}

func (s *Server) switchDiskSession(ctx context.Context, sess *acpSession, sessionName, persistencePath string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "Session name is required.", nil
	}
	if sess.application.Sessions == nil {
		return "Sessions are not enabled.", nil
	}
	loaded, err := sess.application.Sessions.Load(ctx, session.SessionID(sessionName))
	if err != nil {
		return fmt.Sprintf("Could not switch session to %q: %v", sessionName, err), nil
	}
	sess.application.SanitizeLoadedSession(loaded)
	if old := sess.persistID(); old != "" && old != sessionName {
		_ = sess.application.Sessions.Release(session.SessionID(old))
	}
	if _, _, err := sess.application.Sessions.LoadOrCreateAndAcquireIgnoringExisting(ctx, session.SessionID(sessionName), sess.workDir, sess.cfg.Model); err != nil {
		// Best-effort lock; continue even if lock is already held by us.
		if !strings.Contains(err.Error(), "already open") {
			return fmt.Sprintf("Could not switch session to %q: %v", sessionName, err), nil
		}
	}
	sess.diskID = sessionName
	sess.cfg.Session.DefaultSession = sessionName
	sess.application.Config.Session.DefaultSession = sessionName
	for _, update := range sessionHistoryUpdates(loaded) {
		s.emitUpdate(sess.id, update)
	}
	msg := fmt.Sprintf("Switched session to %s.", sessionName)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"session": map[string]any{"default_session": sessionName}}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist default session: %v", err)
	}
	if u := sessionUsageUpdate(ctx, sess.application, loaded, sess.cfg.MaxContextTokens); u != nil {
		s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "usage_update", Usage: u})
	}
	return msg, nil
}

func (s *Server) toggleSkill(sess *acpSession, skillName, persistencePath string) (string, error) {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return "Skill name is required.", nil
	}
	next := sess.cfg
	currentlyEnabled := false
	if sess.application.SkillRegistry != nil {
		_, currentlyEnabled = sess.application.SkillRegistry.Find(skillName)
	}
	if currentlyEnabled {
		next.Skills.Disabled = appendUnique(next.Skills.Disabled, skillName)
		next.Skills.Enabled = removeString(next.Skills.Enabled, skillName)
	} else {
		registry := skill.LoadFromDirs(next.Skills.Paths...)
		if _, ok := registry.Find(skillName); !ok {
			return fmt.Sprintf("Skill %q is not available in configured skill directories.", skillName), nil
		}
		next.Skills.Disabled = removeString(next.Skills.Disabled, skillName)
		if len(next.Skills.Enabled) > 0 {
			next.Skills.Enabled = appendUnique(next.Skills.Enabled, skillName)
		}
	}
	if err := next.Normalize(); err != nil {
		return fmt.Sprintf("Could not update skill %q: %v", skillName, err), nil
	}
	if err := sess.application.ReloadFeatures(next, nil); err != nil {
		return fmt.Sprintf("Could not reload skills after updating %q: %v", skillName, err), nil
	}
	sess.cfg = next
	msg := fmt.Sprintf("Disabled skill %s.", skillName)
	if !currentlyEnabled {
		msg = fmt.Sprintf("Enabled skill %s.", skillName)
	}
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"skills": map[string]any{"enabled": next.Skills.Enabled, "disabled": next.Skills.Disabled}}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist skill toggle: %v", err)
	}
	return msg, nil
}

func (s *Server) toggleMCP(sess *acpSession, serverName, persistencePath string) (string, error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return "MCP server name is required.", nil
	}
	next := sess.cfg
	servers := cloneMCPServers(next.MCP.Servers)
	index := -1
	for i := range servers {
		if servers[i].Name == serverName {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Sprintf("MCP server %q not found.", serverName), nil
	}
	servers[index].Disabled = !servers[index].Disabled
	if !servers[index].Disabled {
		if err := sess.application.CheckMCPServer(servers[index], nil); err != nil {
			return fmt.Sprintf("Could not enable MCP server %q: %v", serverName, err), nil
		}
	}
	next.MCP.Servers = servers
	next.MCPServers = cloneMCPServers(servers)
	if err := next.Normalize(); err != nil {
		return fmt.Sprintf("Could not update MCP server %q: %v", serverName, err), nil
	}
	if err := sess.application.ReloadFeatures(next, nil); err != nil {
		return fmt.Sprintf("Could not reload MCP after updating %q: %v", serverName, err), nil
	}
	sess.cfg = next
	msg := fmt.Sprintf("Enabled MCP server %s.", serverName)
	if servers[index].Disabled {
		msg = fmt.Sprintf("Disabled MCP server %s.", serverName)
	}
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"mcp": map[string]any{"servers": next.MCP.Servers}}); err != nil {
		msg += fmt.Sprintf("\nWarning: could not persist MCP toggle: %v", err)
	}
	return msg, nil
}

func (s *Server) requestChoice(ctx context.Context, sess *acpSession, title, question string, options []PermissionOption) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options available")
	}
	// Cap very large lists so clients stay responsive.
	if len(options) > 40 {
		options = options[:40]
	}
	var result RequestPermissionResult
	err := s.conn.Call(ctx, MethodSessionRequestPermission, RequestPermissionParams{
		SessionID: sess.id,
		ToolCall: ToolCallUpdate{
			ToolCallID: s.nextToolCallID(),
			Title:      title,
			Kind:       "other",
			Status:     ToolCallPending,
			Content: []ToolCallContent{{
				Type:    "content",
				Content: &ContentBlock{Type: "text", Text: question},
			}},
		},
		Options: options,
	}, &result)
	if err != nil {
		return "", err
	}
	switch result.Outcome.OptionID {
	case "", PermissionRejectOnce, PermissionRejectAlways:
		return "", context.Canceled
	default:
		return result.Outcome.OptionID, nil
	}
}

func slashWorkflowsText(sess *acpSession) string {
	if sess == nil || sess.application == nil {
		return "No workflows loaded."
	}
	defs := sess.application.ListWorkflows()
	if len(defs) == 0 {
		return "No workflows loaded. Place YAML under ~/.solcode/workflows or <project>/.solcode/workflows."
	}
	var b strings.Builder
	b.WriteString("Loaded workflows:\n")
	for _, def := range defs {
		desc := strings.TrimSpace(def.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("  %s — %s\n", def.Name, desc))
	}
	b.WriteString("\nRun workflows from the TUI with /workflow <name>.")
	return strings.TrimSpace(b.String())
}

func sanitizeSessionName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeString(values []string, value string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:0]
	for _, existing := range values {
		if existing != value {
			out = append(out, existing)
		}
	}
	return out
}

func cloneMCPServers(servers []config.MCPServerConfig) []config.MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]config.MCPServerConfig, len(servers))
	copy(out, servers)
	return out
}

// usageUpdateFromSession builds a TUI-aligned occupancy update.
// estimatedContextTokens should come from App.EstimateSessionContextTokens
// (same path as the TUI ctx meter). maxContextTokens is cfg.MaxContextTokens.
func usageUpdateFromSession(estimatedContextTokens, maxContextTokens int64) *UsageUpdate {
	return usageUpdate(engine.Usage{
		EstimatedContextTokens: estimatedContextTokens,
		MaxContextTokens:       maxContextTokens,
	})
}

func sessionUsageUpdate(ctx context.Context, application *app.App, s *session.Session, maxContextTokens int64) *UsageUpdate {
	if s == nil {
		return nil
	}
	var estimated int64
	if application != nil {
		estimated = application.EstimateSessionContextTokens(ctx, s)
	}
	return usageUpdateFromSession(estimated, maxContextTokens)
}

func stopReasonFromErr(ctx context.Context, err error) string {
	if err == nil {
		return StopReasonEndTurn
	}
	if ctx.Err() != nil || errorsIsCanceled(err) {
		return StopReasonCancelled
	}
	return StopReasonEndTurn
}

func stopReasonFromResult(ctx context.Context, resultErr string) string {
	if ctx.Err() != nil {
		return StopReasonCancelled
	}
	if strings.TrimSpace(resultErr) != "" {
		return StopReasonRefusal
	}
	return StopReasonEndTurn
}

func errIfNotCancel(ctx context.Context, err error) error {
	if err == nil || errorsIsCanceled(err) || ctx.Err() != nil {
		return nil
	}
	return err
}

func errorsIsCanceled(err error) bool {
	return err != nil && (err == context.Canceled || err == context.DeadlineExceeded || strings.Contains(err.Error(), "canceled") || strings.Contains(err.Error(), "cancelled"))
}
