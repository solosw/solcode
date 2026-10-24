package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/codegraphruntime/bundle"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

type graphFakeClient struct {
	started, closed, calls atomic.Int32
	start                  func(context.Context) error
	life                   context.Context
	callErr                error
	call                   func(context.Context, string, json.RawMessage) (*tool.ContentBlock, error)
}

func (f *graphFakeClient) Start(ctx context.Context) error {
	f.life = ctx
	f.started.Add(1)
	if f.start != nil {
		return f.start(ctx)
	}
	return nil
}
func (f *graphFakeClient) ListTools(context.Context) ([]tool.MCPToolInfo, error) { return nil, nil }
func (f *graphFakeClient) CallTool(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
	f.calls.Add(1)
	if f.call != nil {
		return f.call(ctx, name, input)
	}
	if f.callErr != nil {
		return nil, f.callErr
	}
	return tool.Result(name + ":" + string(input)), nil
}
func (f *graphFakeClient) Close() error { f.closed.Add(1); return nil }

func graphTestClient(t *testing.T, f *graphFakeClient) *CodeGraphClient {
	t.Helper()
	c := NewCodeGraphClient(t.TempDir(), "", "", func(server config.MCPServerConfig) Client {
		if server.Command != "native-codegraph" || !strings.Contains(strings.Join(server.Args, " "), "serve --mcp --path") {
			t.Error("incorrect launch", server)
		}
		for k, v := range map[string]string{"CODEGRAPH_TELEMETRY": "0", "DO_NOT_TRACK": "1", "CODEGRAPH_NO_PROMPT_HOOK": "1", "CODEGRAPH_NO_DAEMON": "0", "CODEGRAPH_NO_WATCH": "0"} {
			if server.Env[k] != v {
				t.Errorf("env %s=%s", k, server.Env[k])
			}
		}
		root := server.Args[len(server.Args)-1]
		if server.Env["CODEGRAPH_DIR"] != filepath.Join(config.ProjectStateDir(root), "codegraph") {
			t.Error("index is not in user project state", server.Env)
		}
		return f
	})
	c.resolve = func(context.Context, string, string) (bundle.Runtime, error) {
		return bundle.Runtime{Binary: "native-codegraph"}, nil
	}
	c.initialize = func(context.Context, bundle.Runtime, string, map[string]string) error { return nil }
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func waitGraph(t *testing.T, c *CodeGraphClient) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("startup hung")
	}
}
func TestCodeGraphStartsProactivelyAndQueryContextDoesNotOwnProcess(t *testing.T) {
	f := &graphFakeClient{}
	c := graphTestClient(t, f)
	c.Start()
	waitGraph(t, c)
	if f.started.Load() != 1 {
		t.Fatal("not started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, err := c.CallTool(ctx, "codegraph_explore", json.RawMessage(`{"query":"main"}`))
	cancel()
	if err != nil || r.IsError || !strings.Contains(r.Text, "codegraph_explore") {
		t.Fatalf("%+v %v", r, err)
	}
	if f.life.Err() != nil {
		t.Fatal("query cancellation killed session")
	}
	_ = c.Close()
	_ = c.Close()
	if f.closed.Load() != 1 || f.life.Err() == nil {
		t.Fatal("close did not release exactly this session")
	}
}
func TestCodeGraphInitializingDoesNotBlockQuery(t *testing.T) {
	f := &graphFakeClient{}
	c := graphTestClient(t, f)
	entered := make(chan struct{})
	c.initialize = func(ctx context.Context, _ bundle.Runtime, _ string, _ map[string]string) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	c.Start()
	<-entered
	r, err := c.CallTool(context.Background(), "codegraph_status", json.RawMessage("{}"))
	if err != nil || !strings.Contains(r.Text, "background") {
		t.Fatalf("%+v %v", r, err)
	}
	_ = c.Close()
	if f.started.Load() != 0 {
		t.Fatal("started proxy after cancellation")
	}
}
func TestCodeGraphUnavailableFallbackAndRoutingRestrictions(t *testing.T) {
	c := graphTestClient(t, &graphFakeClient{})
	c.resolve = func(context.Context, string, string) (bundle.Runtime, error) {
		return bundle.Runtime{}, errors.New("missing runtime")
	}
	c.Start()
	waitGraph(t, c)
	r, err := c.CallTool(context.Background(), "codegraph_status", json.RawMessage("{}"))
	if err != nil || !strings.Contains(r.Text, "missing runtime") || !strings.Contains(r.Text, "Grep") {
		t.Fatalf("%+v %v", r, err)
	}
	for _, tc := range []struct{ name, raw string }{{"codegraph_init", "{}"}, {"codegraph_explore", `{"projectPath":"/other"}`}} {
		r, err := c.CallTool(context.Background(), tc.name, json.RawMessage(tc.raw))
		if err != nil || !r.IsError {
			t.Fatalf("accepted forbidden request: %+v %v", r, err)
		}
	}
}
func TestCodeGraphCloseCancelsMCPStartup(t *testing.T) {
	entered := make(chan struct{})
	f := &graphFakeClient{start: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	c := graphTestClient(t, f)
	c.Start()
	<-entered
	_ = c.Close()
	if f.closed.Load() != 1 {
		t.Fatal("failed startup client leaked")
	}
}
func TestCodeGraphSeparateSessionsCloseIndependently(t *testing.T) {
	f1, f2 := &graphFakeClient{}, &graphFakeClient{}
	a, b := graphTestClient(t, f1), graphTestClient(t, f2)
	a.Start()
	b.Start()
	waitGraph(t, a)
	waitGraph(t, b)
	_ = a.Close()
	if f2.closed.Load() != 0 || f2.life.Err() != nil {
		t.Fatal("closed another session")
	}
}
func TestCodeGraphLogBound(t *testing.T) {
	var b boundedLog
	_, _ = b.Write([]byte(strings.Repeat("x", 10000)))
	_, _ = b.Write([]byte("tail"))
	if len(b.data) != 8192 || !strings.HasSuffix(string(b.data), "tail") {
		t.Fatal("unbounded log")
	}
}

func TestCodeGraphCloseBeforeStartNeverInitializes(t *testing.T) {
	c := graphTestClient(t, &graphFakeClient{})
	c.resolve = func(context.Context, string, string) (bundle.Runtime, error) {
		t.Error("resolved runtime during close")
		return bundle.Runtime{}, nil
	}
	_ = c.Close()
	c.Start()
	r, err := c.CallTool(context.Background(), "codegraph_status", json.RawMessage("{}"))
	if err != nil || !r.IsError || !strings.Contains(r.Text, "closed") {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestCodeGraphTransportFailureDegradesWithoutReplay(t *testing.T) {
	f := &graphFakeClient{}
	c := graphTestClient(t, f)
	c.Start()
	waitGraph(t, c)
	f.callErr = errors.New("transport lost")
	f.calls.Store(0)
	for i := 0; i < 2; i++ {
		r, err := c.CallTool(context.Background(), "codegraph_status", json.RawMessage("{}"))
		if err != nil || !strings.Contains(r.Text, "transport lost") || !strings.Contains(r.Text, "Grep") {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if f.calls.Load() != 1 || f.closed.Load() != 1 {
		t.Fatal("replayed query or leaked transport")
	}
}

func TestCodeGraphLivePreparationWaitsForConcurrentInit(t *testing.T) {
	f := &graphFakeClient{}
	c := graphTestClient(t, f)
	entered, release := make(chan struct{}), make(chan struct{})
	f.call = func(ctx context.Context, name string, raw json.RawMessage) (*tool.ContentBlock, error) {
		var args map[string]string
		if err := json.Unmarshal(raw, &args); err != nil || args["projectPath"] == "" || name != "codegraph_status" {
			t.Errorf("unexpected bootstrap: %s %s", name, raw)
		}
		if f.calls.Load() == 1 {
			return tool.ErrorResult(fmt.Sprintf("Error: No indexed project found for projectPath %q. Pass an absolute path to an indexed project, or run `codegraph init` there.", args["projectPath"])), nil
		}
		if f.calls.Load() == 2 {
			root := args["projectPath"]
			return tool.ErrorResult(fmt.Sprintf("Error: Failed to prepare live synchronization for %s: refusing live services for unindexed project %s; run `codegraph init %s` first", root, root, root)), nil
		}
		if f.calls.Load() == 3 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return tool.Result("live service prepared"), nil
	}
	c.Start()
	select {
	case <-entered:
	case <-c.done:
		t.Fatalf("transient startup failure was not retried: %v", c.err)
	case <-time.After(5 * time.Second):
		t.Fatal("live preparation hung")
	}
	select {
	case <-c.done:
		t.Fatal("reported ready before live catch-up")
	default:
	}
	close(release)
	waitGraph(t, c)
	if c.err != nil || f.started.Load() != 1 || f.closed.Load() != 0 || f.calls.Load() != 3 {
		t.Fatalf("startup did not recover on the same proxy: %v", c.err)
	}
}

func TestCodeGraphCloseCancelsConcurrentInitWait(t *testing.T) {
	for _, source := range []string{"resolver", "live broker"} {
		t.Run(source, func(t *testing.T) {
			f := &graphFakeClient{}
			c := graphTestClient(t, f)
			entered := make(chan struct{})
			f.call = func(_ context.Context, _ string, raw json.RawMessage) (*tool.ContentBlock, error) {
				var args map[string]string
				_ = json.Unmarshal(raw, &args)
				if f.calls.Load() == 1 {
					close(entered)
				}
				root := args["projectPath"]
				if source == "live broker" {
					return tool.ErrorResult(fmt.Sprintf("Failed to prepare live synchronization for %s: refusing live services for unindexed project %s; run `codegraph init %s` first", root, root, root)), nil
				}
				return tool.ErrorResult(fmt.Sprintf("No indexed project found for projectPath %q.", root)), nil
			}
			c.Start()
			<-entered
			_ = c.Close()
			if !errors.Is(c.err, context.Canceled) || f.closed.Load() != 1 || f.calls.Load() != 1 {
				t.Fatalf("startup retry ignored cancellation or leaked proxy: %v", c.err)
			}
		})
	}
}

func TestCodeGraphLivePreparationDeadline(t *testing.T) {
	for _, source := range []string{"resolver", "live broker"} {
		t.Run(source, func(t *testing.T) {
			f := &graphFakeClient{}
			c := graphTestClient(t, f)
			ctx, cancel := context.WithTimeout(c.ctx, 50*time.Millisecond)
			defer cancel()
			c.ctx = ctx
			f.call = func(_ context.Context, _ string, raw json.RawMessage) (*tool.ContentBlock, error) {
				var args map[string]string
				_ = json.Unmarshal(raw, &args)
				root := args["projectPath"]
				if source == "live broker" {
					return tool.ErrorResult(fmt.Sprintf("Failed to prepare live synchronization for %s: refusing live services for unindexed project %s; run `codegraph init %s` first", root, root, root)), nil
				}
				return tool.ErrorResult(fmt.Sprintf("No indexed project found for projectPath %q.", root)), nil
			}
			c.Start()
			waitGraph(t, c)
			if !errors.Is(c.err, context.DeadlineExceeded) || f.closed.Load() != 1 || f.calls.Load() != 1 {
				t.Fatalf("startup retry was not deadline bounded: %v", c.err)
			}
		})
	}
}

func TestCodeGraphLivePreparationDoesNotRetryPermanentFailures(t *testing.T) {
	for _, failure := range []struct {
		name   string
		result *tool.ContentBlock
		err    error
	}{
		{"other project", tool.ErrorResult(`No indexed project found for projectPath "/another/project".`), nil},
		{"other live project", tool.ErrorResult("Failed to prepare live synchronization for /another/project: refusing live services for unindexed project /another/project; run `codegraph init /another/project` first"), nil},
		{"owner mismatch", tool.ErrorResult("index owner mismatch"), nil},
		{"corrupt index", tool.ErrorResult("corrupt index state"), nil},
		{"transport", nil, errors.New("transport lost")},
		{"empty response", nil, nil},
	} {
		t.Run(failure.name, func(t *testing.T) {
			f := &graphFakeClient{call: func(context.Context, string, json.RawMessage) (*tool.ContentBlock, error) {
				return failure.result, failure.err
			}}
			c := graphTestClient(t, f)
			c.Start()
			waitGraph(t, c)
			if c.err == nil || f.calls.Load() != 1 || f.closed.Load() != 1 {
				t.Fatal("retried a permanent failure or leaked proxy", c.err)
			}
		})
	}
}

func TestCodeGraphLiveServicesArePreparedBeforeReady(t *testing.T) {
	f := &graphFakeClient{}
	c := graphTestClient(t, f)
	f.call = func(_ context.Context, name string, raw json.RawMessage) (*tool.ContentBlock, error) {
		var args map[string]string
		if err := json.Unmarshal(raw, &args); err != nil || len(args) != 1 || args["projectPath"] == "" || name != "codegraph_status" {
			t.Errorf("unrecognized live-service bootstrap: %s %s", name, raw)
		}
		return tool.ErrorResult("shared service unavailable"), nil
	}
	c.Start()
	waitGraph(t, c)
	if c.err == nil || f.closed.Load() != 1 || f.calls.Load() != 1 {
		t.Fatal("reported ready without live synchronization", c.err)
	}
	r, err := c.CallTool(context.Background(), "codegraph_status", json.RawMessage("{}"))
	if err != nil || !strings.Contains(r.Text, "Grep") || f.calls.Load() != 1 {
		t.Fatal("failed service preparation did not degrade", r, err)
	}
}

func TestCodeGraphCloseCancelsLiveServicePreparation(t *testing.T) {
	entered := make(chan struct{})
	f := &graphFakeClient{call: func(ctx context.Context, _ string, _ json.RawMessage) (*tool.ContentBlock, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	c := graphTestClient(t, f)
	c.Start()
	<-entered
	_ = c.Close()
	if f.closed.Load() != 1 {
		t.Fatal("live-service preparation leaked its proxy")
	}
}
func TestCodeGraphIndexUsesUserProjectState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	path := codeGraphEnv(root)["CODEGRAPH_DIR"]
	want := filepath.Join(config.ProjectStateDir(canonical), "codegraph")
	if path != want || !filepath.IsAbs(path) {
		t.Fatalf("index=%q want=%q", path, want)
	}
	if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
		t.Fatal("index is inside project")
	}
	if codeGraphEnv(t.TempDir())["CODEGRAPH_DIR"] == path {
		t.Fatal("different projects share an index")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("environment resolution created an index")
	}
}
