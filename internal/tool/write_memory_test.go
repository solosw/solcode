package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type unifiedMemoryWriter struct {
	facts []MemoryWriteRequest
	logs  []SessionMemoryWriteRequest
	err   error
}

func (w *unifiedMemoryWriter) WriteMemory(_ context.Context, req MemoryWriteRequest) (MemoryWriteResult, error) {
	w.facts = append(w.facts, req)
	return MemoryWriteResult{Stored: true, Kind: req.Kind, Scope: req.Scope, Tier: "M4"}, w.err
}
func (w *unifiedMemoryWriter) WriteSessionMemory(_ context.Context, req SessionMemoryWriteRequest) (SessionMemoryWriteResult, error) {
	w.logs = append(w.logs, req)
	return SessionMemoryWriteResult{Stored: true, SessionID: req.SessionID, Turn: 3, Importance: req.Importance}, w.err
}
func TestUnifiedWriteMemoryRoutesDestinations(t *testing.T) {
	writer := &unifiedMemoryWriter{}
	entry := NewWriteMemoryTool(writer, writer)
	uctx := &UseContext{SessionID: "s1", WorkDir: t.TempDir()}
	for _, input := range []string{
		`{"memory":"Use Go tests.","tags":["Build","build"],"scope":"session"}`,
		`{"source":"session_memory","memory":"Problem: merge writes. Status: fixed and tested.","tags":["Memory","memory"],"importance":0.7}`,
	} {
		result, err := entry.Invoke(t.Context(), uctx, json.RawMessage(input))
		if err != nil || result.IsError {
			t.Fatalf("write=%+v err=%v", result, err)
		}
	}
	if len(writer.facts) != 1 || len(writer.logs) != 1 {
		t.Fatalf("facts=%d logs=%d", len(writer.facts), len(writer.logs))
	}
	if writer.facts[0].Kind != "fact" || writer.facts[0].Scope != "session" || !reflect.DeepEqual(writer.facts[0].Tags, []string{"build"}) {
		t.Fatal("archival defaults or scope changed")
	}
	log := writer.logs[0]
	if log.SessionID != "s1" || log.WorkDir != uctx.WorkDir || log.Importance != 0.7 || !reflect.DeepEqual(log.Keywords, []string{"memory"}) || !strings.Contains(log.Summary, "Status:") {
		t.Fatalf("log=%+v", log)
	}
}
func TestUnifiedWriteMemoryValidationAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		source    string
		length    int
		wantError bool
	}{
		{"memory", 1200, false}, {"memory", 1201, true}, {"session_memory", 2000, false}, {"session_memory", 2001, true}, {"invalid", 10, true}, {"session_memory", 0, true},
	} {
		writer := &unifiedMemoryWriter{}
		payload, _ := json.Marshal(WriteMemoryParams{Source: tc.source, Memory: strings.Repeat("中", tc.length)})
		result, err := NewWriteMemoryTool(writer, writer).Invoke(t.Context(), nil, payload)
		if err != nil || result.IsError != tc.wantError {
			t.Fatalf("source=%s length=%d result=%+v err=%v", tc.source, tc.length, result, err)
		}
		if tc.wantError && len(writer.facts)+len(writer.logs) != 0 {
			t.Fatal("invalid input wrote a memory")
		}
	}
	writer := &unifiedMemoryWriter{}
	entry := NewWriteMemoryTool(nil, writer)
	result, err := entry.Invoke(t.Context(), nil, json.RawMessage(`{"source":"session_memory","memory":"Problem: disabled archival memory. Status: session log still works."}`))
	if err != nil || result.IsError || len(writer.logs) != 1 {
		t.Fatal("session log depends on archival memory")
	}
	result, _ = entry.Invoke(t.Context(), nil, json.RawMessage(`{"memory":"durable fact"}`))
	if !result.IsError {
		t.Fatal("disabled archival memory accepted write")
	}
	for _, field := range []string{"kind", "scope", "reason"} {
		payload, _ := json.Marshal(map[string]any{"source": "session_memory", "memory": "a log", field: "fact"})
		result, _ = entry.Invoke(t.Context(), nil, payload)
		if !result.IsError || len(writer.logs) != 1 {
			t.Fatal("session source silently ignored archival field", field)
		}
	}
	result, _ = NewWriteMemoryTool(writer).Invoke(t.Context(), nil, json.RawMessage(`{"source":"session_memory","memory":"a log"}`))
	if !result.IsError {
		t.Fatal("missing session writer accepted write")
	}
	writer.err = errors.New("store unavailable")
	for _, source := range []string{"memory", "session_memory"} {
		payload, _ := json.Marshal(WriteMemoryParams{Source: source, Memory: "remember this"})
		result, _ = NewWriteMemoryTool(writer, writer).Invoke(t.Context(), nil, payload)
		if !result.IsError || !strings.Contains(result.Text, "store unavailable") {
			t.Fatal("store failure not propagated")
		}
	}
}
func TestUnifiedWriteMemoryHidesLegacyDiscovery(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewWriteMemoryTool(nil), NewWriteSessionMemoryTool(nil))
	matches, err := SearchCapabilities(registry, nil, json.RawMessage(`{"query":"WriteSessionMemory session memory","kind":"tool","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, match := range matches {
		if match.Name == WriteSessionMemoryToolName {
			t.Fatal("legacy tool leaked through discovery")
		}
		if match.Name == WriteMemoryToolName {
			found = true
		}
	}
	if !found || registry.Find(WriteSessionMemoryToolName) == nil {
		t.Fatal("unified tool missing or legacy replay broken")
	}
}
