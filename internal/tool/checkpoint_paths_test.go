package tool

import (
	"encoding/json"
	"testing"
)

func TestPathsForCheckpoint(t *testing.T) {
	paths := PathsForCheckpoint(WriteToolName, json.RawMessage(`{"path":"a.go","content":"x"}`))
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Fatalf("Write paths = %#v", paths)
	}
	paths = PathsForCheckpoint(MultiEditToolName, json.RawMessage(`{"edits":[{"path":"a.go","old_string":"a","new_string":"b"},{"path":"a.go","old_string":"b","new_string":"c"},{"path":"b.go","old_string":"x","new_string":"y"}]}`))
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("MultiEdit paths = %#v", paths)
	}
	if CheckpointableFileTool(BashToolName) {
		t.Fatal("bash should not be path-checkpointable")
	}
	if !FingerprintCheckpointTool(BashToolName) {
		t.Fatal("bash should use fingerprint checkpoints")
	}
}
