package agent

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed roles/*.toml
var builtinRoles embed.FS

// RoleDefinition configures behavior, not authority. The caller must intersect
// AllowedTools with the parent's ceiling and the instance's persisted ceiling.
type RoleDefinition struct {
	Description  string   `toml:"description"`
	Instructions string   `toml:"instructions"`
	Model        string   `toml:"model"`
	AllowedTools []string `toml:"allowed_tools"`
}

var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func IsBuiltinType(name string) bool {
	switch name {
	case "explorer", "worker", "reviewer":
		return true
	}
	return false
}

// LoadRole layers the embedded definition, user overrides, then project
// overrides. Unknown names are custom roles and require an override file.
func LoadRole(name, workDir string) (RoleDefinition, error) {
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".solcode", "roles"))
	}
	if strings.TrimSpace(workDir) != "" {
		dirs = append(dirs, filepath.Join(workDir, ".solcode", "roles"))
	}
	return loadRole(name, dirs)
}

func loadRole(name string, dirs []string) (RoleDefinition, error) {
	var role RoleDefinition
	if !roleNamePattern.MatchString(name) {
		return role, fmt.Errorf("invalid agent_type: %q", name)
	}
	found := false
	decode := func(data []byte, source string) error {
		// Decode into a fresh value so slices are replaced, never appended, and
		// omitted keys preserve the previous layer.
		var patch struct {
			Description  *string   `toml:"description"`
			Instructions *string   `toml:"instructions"`
			Model        *string   `toml:"model"`
			AllowedTools *[]string `toml:"allowed_tools"`
		}
		if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&patch); err != nil {
			return fmt.Errorf("load agent role %s: %w", source, err)
		}
		if patch.Description != nil {
			role.Description = *patch.Description
		}
		if patch.Instructions != nil {
			role.Instructions = *patch.Instructions
		}
		if patch.Model != nil {
			role.Model = strings.TrimSpace(*patch.Model)
		}
		if patch.AllowedTools != nil {
			role.AllowedTools = *patch.AllowedTools
		}
		found = true
		return nil
	}
	if data, err := builtinRoles.ReadFile("roles/" + name + ".toml"); err == nil {
		if err := decode(data, "builtin/"+name); err != nil {
			return role, err
		}
	} else if !os.IsNotExist(err) {
		return role, err
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, name+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return role, fmt.Errorf("load agent role %s: %w", path, err)
		}
		if err := decode(data, path); err != nil {
			return role, err
		}
	}
	if !found {
		return role, fmt.Errorf("unknown agent_type: %s", name)
	}
	return role, nil
}
