package acp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

type v2PromptRecord struct {
	MessageID       string            `json:"messageId"`
	Key             string            `json:"idempotencyKey,omitempty"`
	Content         []json.RawMessage `json:"content"`
	TranscriptIndex int               `json:"transcriptIndex"`
	Fingerprint     string            `json:"fingerprint,omitempty"`
}

type v2PromptResult struct {
	MessageID string `json:"messageId"`
}

func (s *Server) prepareV2Prompt(ctx context.Context, msg jsonrpcMessage) (func() error, error) {
	var params struct {
		SessionID string            `json:"sessionId"`
		Prompt    []json.RawMessage `json:"prompt"`
		Key       *string           `json:"idempotencyKey"`
	}
	if err := decodeParams(msg.Params, &params); err != nil {
		return nil, s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
	}
	sess := s.getSession(params.SessionID)
	if sess == nil {
		return nil, s.v2UnknownSession(msg, params.SessionID)
	}
	key := ""
	if params.Key != nil {
		key = *params.Key
		if key == "" || utf8.RuneCountInString(key) > 255 {
			return nil, s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, "idempotencyKey must contain 1 to 255 characters")
		}
		sess.mu.Lock()
		for _, record := range sess.v2Prompts {
			if record.Key == key {
				sess.mu.Unlock()
				return nil, s.conn.Reply(msg.ID, v2PromptResult{record.MessageID})
			}
		}
		sess.mu.Unlock()
	}
	sess.mu.Lock()
	busy := sess.prompting || sess.closing
	sess.mu.Unlock()
	if busy {
		return nil, s.v2Busy(msg)
	}
	blocks := make([]ContentBlock, len(params.Prompt))
	for i, raw := range params.Prompt {
		if err := json.Unmarshal(raw, &blocks[i]); err != nil {
			return nil, s.conn.ReplyError(msg.ID, JSONRPCInvalidParams, err.Error())
		}
	}
	prompt, inline, err := sess.v2PromptText(blocks)
	if err != nil {
		return nil, s.conn.ReplyError(msg.ID, configErrorCode(err), err.Error())
	}
	cmd, slash := parseSlashCommand(prompt)
	builtin := !inline && slash && isBuiltinSlashCommand(cmd.Name)
	if !inline && !builtin {
		expanded, err := sess.expandSkillCommand(prompt)
		if err != nil {
			return nil, s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
		inline = expanded != prompt
		prompt = expanded
	}
	if err := sess.applyPendingConfig(); err != nil {
		return nil, s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
	}
	record := v2PromptRecord{
		MessageID: fmt.Sprintf("user-%d", len(sess.v2Prompts)+1), Key: key, Content: params.Prompt,
	}
	if sess.application.Sessions != nil {
		current, err := sess.application.Sessions.Load(ctx, session.SessionID(sess.persistID()))
		if err != nil && !session.IsNotFound(err) {
			return nil, s.conn.ReplyError(msg.ID, JSONRPCInternalError, err.Error())
		}
		if current != nil {
			record.TranscriptIndex = len(current.Messages)
		}
	}
	if !builtin && s.runPrompt != nil {
		record.Fingerprint = v2Fingerprint(attach.UserMessage(attach.Expand(prompt, sess.workDir)))
	}
	runCtx, cancel := conversationContext(ctx, s.timeout)
	if !sess.beginPrompt(runCtx, cancel) {
		cancel()
		return nil, s.v2Busy(msg)
	}
	// Acceptance is durable before acknowledgement, including local commands
	// and prompts cancelled before the engine can save its first checkpoint.
	sess.mu.Lock()
	recordIndex := len(sess.v2Prompts)
	err = sess.saveV2PromptsLocked(append(sess.v2Prompts, record))
	sess.mu.Unlock()
	if err != nil {
		sess.endPrompt()
		return nil, s.conn.ReplyError(msg.ID, JSONRPCInternalError, "save accepted prompt: "+err.Error())
	}
	if !builtin && sess.application.Sessions != nil {
		runCtx = app.WithSessionPromptObserver(runCtx, func(index int, userMessage sdk.MessageParam) error {
			return sess.linkV2Prompt(recordIndex, index, userMessage)
		})
	}
	if err := s.conn.Reply(msg.ID, v2PromptResult{record.MessageID}); err != nil {
		sess.endPrompt()
		return nil, err
	}
	return func() error {
		s.emitV2UserPrompt(sess.id, record, true)
		s.emitV2State(sess.id, "running", "", nil)
		return s.runSessionPrompt(runCtx, msg, sess, prompt, inline)
	}, nil
}

func (sess *acpSession) saveV2PromptsLocked(records []v2PromptRecord) error {
	if sess.application.Sessions != nil {
		if err := config.SaveLocalOverrides(v2PromptPath(sess.cfg.Session.Dir, sess.id), map[string]any{"prompts": records}); err != nil {
			return err
		}
	}
	sess.v2Prompts = records
	return nil
}

func (sess *acpSession) linkV2Prompt(recordIndex, transcriptIndex int, userMessage sdk.MessageParam) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	records := append([]v2PromptRecord(nil), sess.v2Prompts...)
	record := &records[recordIndex]
	fingerprint := v2Fingerprint(userMessage)
	if record.TranscriptIndex == transcriptIndex && record.Fingerprint == fingerprint {
		return nil
	}
	record.TranscriptIndex = transcriptIndex
	record.Fingerprint = fingerprint
	if err := sess.saveV2PromptsLocked(records); err != nil {
		return fmt.Errorf("link ACP prompt to transcript: %w", err)
	}
	return nil
}

func v2PromptPath(dir, id string) string {
	hash := sha256.Sum256([]byte(id))
	return filepath.Join(dir, "acp-prompts", fmt.Sprintf("%x.json", hash))
}

func (sess *acpSession) loadV2Prompts() error {
	prompts, err := readV2Prompts(sess.cfg.Session.Dir, sess.id)
	if err != nil {
		return err
	}
	sess.v2Prompts = prompts
	return nil
}

func readV2Prompts(dir, id string) ([]v2PromptRecord, error) {
	body, err := os.ReadFile(v2PromptPath(dir, id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ACP prompt history: %w", err)
	}
	var data struct {
		Prompts []v2PromptRecord `json:"prompts"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode ACP prompt history: %w", err)
	}
	return data.Prompts, nil
}

func v2Fingerprint(msg sdk.MessageParam) string {
	if msg.Role != sdk.MessageParamRoleUser {
		return ""
	}
	// Ignore cache controls and other internal additions. Only actual user
	// input blocks participate in matching the saved protocol message.
	var input []string
	for _, block := range msg.Content {
		if block.OfText != nil {
			input = append(input, "text:"+strings.TrimSpace(block.OfText.Text))
		} else if block.OfImage != nil && block.OfImage.Source.OfBase64 != nil {
			input = append(input, "image:"+block.OfImage.Source.OfBase64.Data)
		}
	}
	if len(input) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(input)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (s *Server) replayV2History(sess *acpSession, current *session.Session) {
	s.mu.Lock()
	structured := s.v2SkillCommands
	s.mu.Unlock()
	history := current.Messages
	fingerprints := make([]string, len(history))
	for i, msg := range history {
		fingerprints[i] = v2Fingerprint(msg)
	}
	next := 0
	emitHistory := func(end int) {
		for _, msg := range history[next:end] {
			for _, update := range sessionHistoryUpdates(&session.Session{Messages: []sdk.MessageParam{msg}}) {
				s.emitUpdate(sess.id, update)
			}
		}
		next = end
	}
	// Match retained input from newest to oldest. Compaction can shift message
	// indices, and may retain only the newest of several identical inputs.
	limit := len(history)
	type replayAnchor struct {
		index   int
		matched bool
	}
	anchors := make([]replayAnchor, len(sess.v2Prompts))
	for i := len(sess.v2Prompts) - 1; i >= 0; i-- {
		record := sess.v2Prompts[i]
		index := record.TranscriptIndex
		if record.Fingerprint != "" {
			if index >= limit || index < 0 || fingerprints[index] != record.Fingerprint {
				for index = limit - 1; index >= 0 && fingerprints[index] != record.Fingerprint; index-- {
				}
			}
			if index >= 0 {
				anchors[i] = replayAnchor{index: index, matched: true}
				limit = index
				continue
			}
		}
		// An accepted prompt may never reach the model. Keep it before the
		// next matching user message when old transcript positions have shifted.
		anchors[i].index = min(max(record.TranscriptIndex, 0), limit)
	}
	for i, record := range sess.v2Prompts {
		anchor := anchors[i]
		emitHistory(max(anchor.index, next))
		s.emitV2UserPrompt(sess.id, record, structured)
		if anchor.matched {
			next++
		}
	}
	emitHistory(len(history))
}

func (s *Server) emitV2UserPrompt(id string, record v2PromptRecord, structured bool) {
	content := record.Content
	if !structured {
		content = append([]json.RawMessage(nil), content...)
		for i, raw := range content {
			var block ContentBlock
			if json.Unmarshal(raw, &block) == nil && block.Type == "skill" {
				content[i], _ = json.Marshal(ContentBlock{Type: "text", Text: "/" + block.Name})
			}
		}
	}
	if !s.isV2() {
		for _, raw := range content {
			var block ContentBlock
			if json.Unmarshal(raw, &block) == nil {
				s.emitUpdate(id, SessionUpdate{SessionUpdate: "user_message_chunk", Content: &block})
			}
		}
		return
	}
	s.notifyV2(id, map[string]any{"sessionUpdate": "user_message", "messageId": record.MessageID, "content": content})
}
