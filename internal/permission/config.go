package permission

import "strings"

type Config struct {
	Mode      Mode     `json:"mode,omitempty"`
	Allow     []string `json:"allow,omitempty"`
	AllowBash []string `json:"allow_bash,omitempty"`
}

func cleanStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
