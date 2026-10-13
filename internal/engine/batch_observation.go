package engine

import (
	"encoding/json"
	"fmt"
)

// Mirror the generated IDs used by BatchTool, including collisions with
// explicit IDs. Local context observers must correlate by ID, not position.
type batchObservedOperation struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

func batchObservedOperations(input []byte) []batchObservedOperation {
	var req struct {
		Operations []batchObservedOperation `json:"operations"`
	}
	if json.Unmarshal(input, &req) != nil {
		return nil
	}
	used := make(map[string]bool)
	for _, op := range req.Operations {
		if op.ID != "" {
			used[op.ID] = true
		}
	}
	for i := range req.Operations {
		if req.Operations[i].ID != "" {
			continue
		}
		id := fmt.Sprintf("op_%d", i+1)
		for used[id] {
			id += "_"
		}
		req.Operations[i].ID = id
		used[id] = true
	}
	return req.Operations
}
