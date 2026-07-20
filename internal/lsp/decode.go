package lsp

import "encoding/json"

func decodeJSON(data []byte, v any) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, v)
}
