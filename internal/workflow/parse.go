package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseFile loads a workflow definition from a YAML or JSON file.
func ParseFile(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	def, err := Parse(data, path)
	if err != nil {
		return Definition{}, err
	}
	def.Path = path
	return def, nil
}

// Parse decodes workflow YAML or JSON bytes.
func Parse(data []byte, pathHint string) (Definition, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Definition{}, fmt.Errorf("empty workflow file")
	}
	var def Definition
	ext := strings.ToLower(filepath.Ext(pathHint))
	switch {
	case ext == ".json" || strings.HasPrefix(trimmed, "{"):
		if err := json.Unmarshal(data, &def); err != nil {
			return Definition{}, fmt.Errorf("parse workflow json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(data, &def); err != nil {
			// Fall back to JSON if YAML fails and content looks like JSON.
			if jsonErr := json.Unmarshal(data, &def); jsonErr != nil {
				return Definition{}, fmt.Errorf("parse workflow yaml: %w", err)
			}
		}
	}
	if strings.TrimSpace(def.Name) == "" && pathHint != "" {
		base := filepath.Base(pathHint)
		// name/workflow.yaml → use parent dir; name.yaml → stem
		if isWorkflowFileName(base) {
			def.Name = filepath.Base(filepath.Dir(pathHint))
		} else {
			def.Name = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}
	def.Name = normalizeName(def.Name)
	if def.Name == "" {
		return Definition{}, fmt.Errorf("workflow name is required")
	}
	if def.Description == "" {
		def.Description = fmt.Sprintf("Workflow %s", def.Name)
	}
	return def, nil
}

func isWorkflowFileName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "workflow.yaml" || lower == "workflow.yml" || lower == "workflow.json"
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	return strings.ToLower(name)
}
