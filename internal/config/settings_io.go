package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// settingsParseError locates malformed JSON without echoing config contents,
// which may include API keys. Preserve the original error for errors.As callers.
func settingsParseError(operation, path string, data []byte, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		offset := max(int64(0), min(syntax.Offset-1, int64(len(data))))
		prefix := data[:offset]
		line := bytes.Count(prefix, []byte{'\n'}) + 1
		column := utf8.RuneCount(prefix[bytes.LastIndexByte(prefix, '\n')+1:]) + 1
		return fmt.Errorf("%s %q at line %d column %d: %w", operation, path, line, column, err)
	}
	return fmt.Errorf("%s %q: %w", operation, path, err)
}

// writeSettingsFile publishes a complete file by renaming a same-directory
// temporary file. ACP processes starting during a settings save must never
// observe the empty/truncated intermediate contents of an in-place write.
// This protects readers, not read/modify/write transactions between processes.
func writeSettingsFile(path string, data []byte) error {
	// Retain existing dotfile symlinks instead of replacing the link itself.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if !os.IsNotExist(err) {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
