package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestAgentPlanUpdateMarshalsTodoWriteEntries(t *testing.T) {
	params := tool.TodoWriteParams{Todos: []tool.TodoItem{
		{Content: "Inspect protocol", Priority: "high", Status: "completed"},
		{Content: "Implement filesystem", Priority: "medium", Status: "in_progress"},
	}}
	entries := make([]PlanEntry, 0, len(params.Todos))
	for _, todo := range params.Todos {
		entries = append(entries, PlanEntry{Content: todo.Content, Priority: todo.Priority, Status: todo.Status})
	}
	raw, err := json.Marshal(SessionUpdate{SessionUpdate: "plan", PlanEntries: entries})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		SessionUpdate string      `json:"sessionUpdate"`
		Entries       []PlanEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionUpdate != "plan" || len(payload.Entries) != 2 {
		t.Fatalf("payload = %s", raw)
	}
	if payload.Entries[1] != (PlanEntry{Content: "Implement filesystem", Priority: "medium", Status: "in_progress"}) {
		t.Fatalf("entries = %#v", payload.Entries)
	}
}

func TestEmitPlanUpdateMapsTodoWrite(t *testing.T) {
	var wire bytes.Buffer
	server := &Server{conn: NewConn(strings.NewReader(""), &wire)}
	sess := &acpSession{id: "session-1"}
	input, err := json.Marshal(tool.TodoWriteParams{Todos: []tool.TodoItem{
		{Content: "Inspect protocol", Priority: "high", Status: "completed"},
		{Content: "Implement filesystem", Priority: "medium", Status: "in_progress"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server.emitPlanUpdate(sess, input)

	var message jsonrpcMessage
	if err := json.Unmarshal(bytes.TrimSpace(wire.Bytes()), &message); err != nil {
		t.Fatalf("decode session update: %v; wire=%s", err, wire.String())
	}
	if message.Method != MethodSessionUpdate {
		t.Fatalf("method = %q", message.Method)
	}
	var params SessionUpdateParams
	if err := decodeParams(message.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.SessionID != sess.id || params.Update.SessionUpdate != "plan" {
		t.Fatalf("update = %+v", params)
	}
	if len(params.Update.PlanEntries) != 2 || params.Update.PlanEntries[1].Status != "in_progress" {
		t.Fatalf("entries = %+v", params.Update.PlanEntries)
	}
}

func TestClientTextFileSystemRespectsCapabilities(t *testing.T) {
	fs := &clientTextFileSystem{capabilities: FSClientCapabilities{}}
	if fs.CanReadTextFile() || fs.CanWriteTextFile() {
		t.Fatal("disabled capabilities reported as enabled")
	}
	if _, err := fs.ReadTextFile(context.Background(), "/tmp/file"); err == nil || !strings.Contains(err.Error(), MethodFSReadTextFile) {
		t.Fatalf("read error = %v", err)
	}
	if err := fs.WriteTextFile(context.Background(), "/tmp/file", "data"); err == nil || !strings.Contains(err.Error(), MethodFSWriteTextFile) {
		t.Fatalf("write error = %v", err)
	}
}

func TestClientTextFileSystemDisablesWriteInBypassMode(t *testing.T) {
	server := &Server{sessions: make(map[string]*acpSession)}
	perms := permission.NewService(permission.ModeBypass)
	sess := &acpSession{
		id:          "session-bypass",
		application: &app.App{Permissions: perms},
	}
	server.sessions[sess.id] = sess

	fs := &clientTextFileSystem{
		server:       server,
		sessionID:    sess.id,
		capabilities: FSClientCapabilities{ReadTextFile: true, WriteTextFile: true},
	}
	if !fs.CanReadTextFile() {
		t.Fatal("read capability should stay enabled in bypass")
	}
	if fs.CanWriteTextFile() {
		t.Fatal("write capability should be disabled in bypass to avoid client fs accept prompts")
	}

	perms.SetMode(permission.ModeAuto)
	if !fs.CanWriteTextFile() {
		t.Fatal("write capability should return when leaving bypass")
	}
}

func TestClientTextFileSystemUltraAvoidsDuplicateWriteApproval(t *testing.T) {
	server := &Server{sessions: make(map[string]*acpSession)}
	perms := permission.NewService(permission.ModeAuto)
	sess := &acpSession{id: "session-ultra", application: &app.App{Permissions: perms}}
	server.sessions[sess.id] = sess
	fs := &clientTextFileSystem{
		server: server, sessionID: sess.id,
		capabilities: FSClientCapabilities{ReadTextFile: true, WriteTextFile: true},
	}
	for _, mode := range []permission.Mode{permission.ModeUltra, "ultrl", permission.ModeBypass, permission.ModeGoal} {
		perms.SetMode(mode)
		if !fs.CanReadTextFile() || fs.CanWriteTextFile() {
			t.Fatalf("mode %s: client read=%v write=%v; want read-only client FS to avoid duplicate approval", mode, fs.CanReadTextFile(), fs.CanWriteTextFile())
		}
	}
	perms.SetMode(permission.ModeAuto)
	if !fs.CanWriteTextFile() {
		t.Fatal("client writes should return when leaving ultra")
	}
}
