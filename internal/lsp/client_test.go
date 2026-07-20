package lsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryLookupByExtension(t *testing.T) {
	reg := NewRegistry(ServerCommand{
		Language:   "go",
		Extensions: []string{"go", ".GO"},
		Command:    []string{"gopls"},
	})
	cmd, ok := reg.Lookup("pkg/main.go")
	if !ok {
		t.Fatal("expected lookup hit")
	}
	if cmd.Language != "go" {
		t.Fatalf("language = %q", cmd.Language)
	}
	if len(cmd.Extensions) != 1 || cmd.Extensions[0] != ".go" {
		t.Fatalf("extensions = %#v", cmd.Extensions)
	}
}

func TestMergeCommandsUserOverridesDefault(t *testing.T) {
	user := []ServerCommand{{
		Language:   "go",
		Extensions: []string{".go"},
		Command:    []string{"custom-gopls"},
	}}
	merged := mergeCommands(user, true)
	if len(merged) < 1 {
		t.Fatal("expected merged commands")
	}
	if merged[0].Command[0] != "custom-gopls" {
		t.Fatalf("expected user command first, got %#v", merged[0].Command)
	}
	foundTS := false
	for _, c := range merged {
		if c.Language == "typescript" {
			foundTS = true
		}
		if c.Language == "go" && c.Command[0] != "custom-gopls" {
			t.Fatalf("default go should be overridden: %#v", c)
		}
	}
	if !foundTS {
		t.Fatal("expected typescript default in merge")
	}
}

func TestParseHoverAndLocations(t *testing.T) {
	hover := parseHover(json.RawMessage(`{"contents":{"kind":"markdown","value":"**T**"}}`))
	if hover != "**T**" {
		t.Fatalf("hover = %q", hover)
	}
	locs := parseLocations(json.RawMessage(`{"uri":"file:///a.go","range":{"start":{"line":2,"character":4},"end":{"line":2,"character":8}}}`))
	if len(locs) != 1 || locs[0].Line != 3 || locs[0].Character != 5 {
		t.Fatalf("locs = %#v", locs)
	}
}

func TestProcessClientFakeLanguageServer(t *testing.T) {
	fake := writeFakeLanguageServer(t)
	work := t.TempDir()
	src := filepath.Join(work, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(ServerCommand{
		Language:   "go",
		Extensions: []string{".go"},
		Command:    fake,
	})
	client := NewProcessClient(reg)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, Request{
		Operation: OperationHover,
		FilePath:  src,
		Line:      3,
		Character: 6,
		WorkDir:   work,
	})
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if !strings.Contains(resp.Text, "fake-hover") {
		t.Fatalf("unexpected hover text: %q", resp.Text)
	}

	resp, err = client.Request(ctx, Request{
		Operation: OperationGoToDefinition,
		FilePath:  src,
		Line:      3,
		Character: 6,
		WorkDir:   work,
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(resp.Locations) != 1 || resp.Locations[0].Line != 3 {
		t.Fatalf("definition locations = %#v", resp.Locations)
	}
}

func writeFakeLanguageServer(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_ls.py")
	py := strings.Join([]string{
		"import json, sys",
		"",
		"def read_message():",
		"    headers = {}",
		"    while True:",
		"        line = sys.stdin.buffer.readline()",
		"        if not line:",
		"            return None",
		"        line = line.decode('utf-8')",
		"        if line in ('\\r\\n', '\\n'):",
		"            break",
		"        if ':' in line:",
		"            k, v = line.split(':', 1)",
		"            headers[k.strip().lower()] = v.strip()",
		"    n = int(headers.get('content-length', '0'))",
		"    body = sys.stdin.buffer.read(n)",
		"    return json.loads(body.decode('utf-8'))",
		"",
		"def write_message(msg):",
		"    raw = json.dumps(msg).encode('utf-8')",
		"    sys.stdout.buffer.write(('Content-Length: %d\\r\\n\\r\\n' % len(raw)).encode('ascii'))",
		"    sys.stdout.buffer.write(raw)",
		"    sys.stdout.buffer.flush()",
		"",
		"while True:",
		"    msg = read_message()",
		"    if msg is None:",
		"        break",
		"    method = msg.get('method')",
		"    mid = msg.get('id')",
		"    if method == 'initialize':",
		"        write_message({'jsonrpc':'2.0','id':mid,'result':{'capabilities':{",
		"            'hoverProvider': True, 'definitionProvider': True,",
		"            'referencesProvider': True, 'documentSymbolProvider': True,",
		"            'workspaceSymbolProvider': True, 'implementationProvider': True}}})",
		"    elif method == 'textDocument/hover':",
		"        write_message({'jsonrpc':'2.0','id':mid,'result':{'contents':{'kind':'markdown','value':'fake-hover'}}})",
		"    elif method == 'textDocument/definition':",
		"        uri = ((msg.get('params') or {}).get('textDocument') or {}).get('uri') or 'file:///x'",
		"        write_message({'jsonrpc':'2.0','id':mid,'result':{'uri':uri,'range':{'start':{'line':2,'character':5},'end':{'line':2,'character':10}}}})",
		"    elif method == 'shutdown':",
		"        write_message({'jsonrpc':'2.0','id':mid,'result': None})",
		"    elif method == 'exit':",
		"        break",
		"    elif mid is not None:",
		"        write_message({'jsonrpc':'2.0','id':mid,'result': None})",
	}, "\n")
	if err := os.WriteFile(script, []byte(py+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("python not available for fake language server")
	}
	return []string{python, script}
}

func TestPathToURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	uri := pathToURI(p)
	if !strings.HasPrefix(uri, "file:") {
		t.Fatalf("uri = %q", uri)
	}
	back := uriToPath(uri)
	if filepath.Clean(back) != filepath.Clean(p) {
		absP, _ := filepath.Abs(p)
		absB, _ := filepath.Abs(back)
		if !strings.EqualFold(filepath.Clean(absP), filepath.Clean(absB)) {
			t.Fatalf("roundtrip path %q -> %q -> %q", p, uri, back)
		}
	}
}

func TestManagerFromCommandsNoServers(t *testing.T) {
	m := NewManagerFromCommands(nil, false)
	defer m.Close()
	_, err := m.Execute(context.Background(), Request{
		Operation: OperationHover,
		FilePath:  "x.go",
		Line:      1,
		Character: 1,
	})
	if err == nil {
		t.Fatal("expected error when no servers")
	}
	if !strings.Contains(err.Error(), "language server is not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateWorkspaceSymbol(t *testing.T) {
	if err := Validate(Request{Operation: OperationWorkspaceSymbol, Query: "Foo"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate(Request{Operation: OperationWorkspaceSymbol}); err == nil {
		t.Fatal("expected error")
	}
}
