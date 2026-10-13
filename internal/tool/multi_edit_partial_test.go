package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiEditPerFileAtomicityAndIndependentContinuation(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("before"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("before"), 0600)
	var changes []FileChange
	out, err := NewMultiEditTool().Invoke(context.Background(), &UseContext{WorkDir: dir, RecordFileChange: func(_ context.Context, c FileChange) { changes = append(changes, c) }}, json.RawMessage(`{"edits":[
  {"path":"a.txt","old_string":"before","new_string":"after"},
  {"path":"./a.txt","old_string":"missing","new_string":"oops"},
  {"path":"b.txt","old_string":"before","new_string":"after"},
  {"path":"b.txt","old_string":"after","new_string":"final"}
 ]}`))
	if err != nil || !out.IsError || !strings.Contains(out.Text, "Applied 2/4 edits") {
		t.Fatalf("result=%+v err=%v", out, err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(a) != "before" || string(b) != "final" || len(changes) != 1 || changes[0].Path != filepath.Join(dir, "b.txt") {
		t.Fatalf("a=%q b=%q changes=%+v", a, b, changes)
	}
}

func TestMultiEditAtomicOptInRejectsWholeTransaction(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("before"), 0600)
	out, err := NewMultiEditTool().Invoke(context.Background(), &UseContext{WorkDir: dir}, json.RawMessage(`{"atomic":true,"edits":[
  {"path":"a.txt","old_string":"before","new_string":"after"},
  {"path":"b.txt","old_string":"missing","new_string":"oops"}
 ]}`))
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || !out.IsError || string(got) != "before" {
		t.Fatalf("out=%+v err=%v got=%q", out, err, got)
	}
}

// batchMemoryFS can model a remote writer failing after altering file contents.
type batchMemoryFS struct {
	files          map[string]string
	readOnly       bool
	failAfterWrite bool
	cancel         context.CancelFunc
}

func (fs *batchMemoryFS) CanReadTextFile() bool  { return true }
func (fs *batchMemoryFS) CanWriteTextFile() bool { return !fs.readOnly }
func (fs *batchMemoryFS) ReadTextFile(_ context.Context, path string) (string, error) {
	text, ok := fs.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return text, nil
}
func (fs *batchMemoryFS) WriteTextFile(ctx context.Context, path, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.files[path] = text
	if fs.failAfterWrite {
		fs.failAfterWrite = false
		if fs.cancel != nil {
			fs.cancel()
		}
		return context.Canceled
	}
	return nil
}

func TestMultiEditRestoresPartiallyWrittenFileAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fs := &batchMemoryFS{files: map[string]string{path: "before"}, failAfterWrite: true, cancel: cancel}
	out, err := NewMultiEditTool().Invoke(ctx, &UseContext{WorkDir: dir, TextFileSystem: fs}, json.RawMessage(`{"edits":[{"path":"a.txt","old_string":"before","new_string":"after"}]}`))
	if err != nil || !out.IsError || !strings.Contains(out.Text, "rolled back") || fs.files[path] != "before" {
		t.Fatalf("out=%+v err=%v file=%q", out, err, fs.files[path])
	}
}
