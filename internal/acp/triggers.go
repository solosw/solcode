package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/trigger"
)

type sessionTriggerKey struct{}

// ensureSessionTriggers is called by the serialized prompt path after slash
// commands. Switching disk sessions detaches the old listener scope first.
func (s *Server) ensureSessionTriggers(sess *acpSession) error {
	if sess.application == nil {
		return nil
	}
	id := sess.persistID()
	sess.mu.Lock()
	if sess.closing {
		sess.mu.Unlock()
		return fmt.Errorf("session is closing")
	}
	if sess.triggerID == id {
		sess.mu.Unlock()
		return nil
	}
	detach := sess.triggerDetach
	sess.triggerDetach = nil
	if sess.triggerWake == nil {
		sess.triggerWake = make(chan struct{}, 1)
		sess.triggerStop = make(chan struct{})
		go s.sessionTriggerLoop(sess)
	}
	wake := sess.triggerWake
	sess.mu.Unlock()
	if detach != nil {
		detach()
	}
	detach, err := sess.application.AttachSessionTriggers(id, sess.workDir, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	sess.mu.Lock()
	if sess.closing {
		sess.mu.Unlock()
		detach()
		return fmt.Errorf("session is closing")
	}
	sess.triggerID = id
	sess.triggerDetach = detach
	sess.mu.Unlock()
	return nil
}

func (sess *acpSession) detachTriggerScope() {
	sess.mu.Lock()
	detach := sess.triggerDetach
	sess.triggerDetach = nil
	sess.triggerID = ""
	sess.mu.Unlock()
	if detach != nil {
		detach()
	}
}

func (s *Server) sessionTriggerLoop(sess *acpSession) {
	for {
		select {
		case <-sess.triggerStop:
			return
		case <-sess.triggerWake:
		}
		for {
			sess.mu.Lock()
			busy, closing, done := sess.prompting, sess.closing, sess.promptDone
			sess.mu.Unlock()
			if closing {
				return
			}
			if busy {
				select {
				case <-sess.triggerStop:
					return
				case <-done:
				}
				continue
			}
			ctx, cancel := conversationContext(context.Background(), s.timeout)
			if !sess.beginPrompt(ctx, cancel) {
				cancel()
				continue
			}
			event, ok := sess.application.NextSessionTrigger(sess.persistID(), sess.workDir)
			if !ok {
				sess.endPrompt()
				break
			}
			if s.isV2() {
				raw, _ := json.Marshal(ContentBlock{Type: "text", Text: event.Prompt})
				sess.mu.Lock()
				recordIndex := len(sess.v2Prompts)
				record := v2PromptRecord{MessageID: fmt.Sprintf("user-%d", recordIndex+1), Content: []json.RawMessage{raw}}
				err := sess.saveV2PromptsLocked(append(sess.v2Prompts, record))
				sess.mu.Unlock()
				if err != nil {
					// RunSessionTrigger must not start a model if acceptance could not be saved.
					sess.application.FailSessionTrigger(event, err)
					sess.endPrompt()
					log.Printf("trigger acceptance: %v", err)
					break
				}
				ctx = app.WithSessionPromptObserver(ctx, func(index int, message sdk.MessageParam) error { return sess.linkV2Prompt(recordIndex, index, message) })
				s.emitV2UserPrompt(sess.id, record, true)
				s.emitV2State(sess.id, "running", "", nil)
			} else {
				s.emitText(sess, "user_message_chunk", event.Prompt)
				s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "status_update", Message: "Running session trigger"})
			}
			ctx = context.WithValue(ctx, sessionTriggerKey{}, event)
			if err := s.runSessionPrompt(ctx, jsonrpcMessage{}, sess, event.Prompt, true); err != nil {
				log.Printf("trigger session: %v", err)
			}
		}
	}
}

func triggerEvent(ctx context.Context) (trigger.Event, bool) {
	event, ok := ctx.Value(sessionTriggerKey{}).(trigger.Event)
	return event, ok
}
