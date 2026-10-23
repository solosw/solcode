package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"strings"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/codegraphruntime/bundle"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

// CodeGraphClient owns only this session's proxy. The upstream daemon owns the
// project watcher and multiplexes sessions. Closing a proxy never stops it.
type CodeGraphClient struct {
	mu                      sync.Mutex
	root, runtimeDir, cache string
	factory                 ClientFactory
	resolve                 func(context.Context, string, string) (bundle.Runtime, error)
	initialize              func(context.Context, bundle.Runtime, string, map[string]string) error
	client                  Client
	ctx                     context.Context
	cancel                  context.CancelFunc
	once                    sync.Once
	done                    chan struct{}
	err                     error
	closed                  bool
}

func NewCodeGraphClient(root, runtimeDir, cache string, factory ClientFactory) *CodeGraphClient {
	if factory == nil {
		factory = NewClientFactory()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &CodeGraphClient{root: root, runtimeDir: runtimeDir, cache: cache, factory: factory, resolve: bundle.Resolve, initialize: initializeCodeGraph, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// A separate native-engine namespace preserves any previous Node-engine index.
const codeGraphIndexDir = ".solcode-codegraph"

func codeGraphEnv() map[string]string {
	return map[string]string{
		"DO_NOT_TRACK": "1", "CODEGRAPH_TELEMETRY": "0",
		"CODEGRAPH_DIR": codeGraphIndexDir, "CODEGRAPH_NO_WATCH": "0",
		"CODEGRAPH_NO_DAEMON": "0", "CODEGRAPH_NO_PROMPT_HOOK": "1",
	}
}

type boundedLog struct{ data []byte }

func (b *boundedLog) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 8192
	if n >= limit {
		b.data = append(b.data[:0], p[n-limit:]...)
	} else {
		if len(b.data)+n > limit {
			b.data = append([]byte(nil), b.data[len(b.data)+n-limit:]...)
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func initializeCodeGraph(ctx context.Context, r bundle.Runtime, root string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	// CLI init treats an unowned directory as explicit permission to rebuild.
	// Automatic project opening must never grant that permission to a raw DB.
	dir := filepath.Join(root, codeGraphIndexDir)
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing a symlinked CodeGraph index")
	}
	if _, err := os.Lstat(filepath.Join(dir, "codegraph.db")); err == nil {
		owned := false
		for _, name := range []string{"index-state.0.json", "index-state.1.json"} {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				owned = true
			}
		}
		if !owned {
			return fmt.Errorf("existing unrecognized CodeGraph database; leaving it untouched")
		}
	}
	for attempt := 0; ; attempt++ {
		cmd := exec.CommandContext(ctx, r.Binary, "init", root, "--target", "none", "--yes")
		cmd.Dir = root
		configureCodeGraphProcess(cmd)
		cmd.Env = mergeProcessEnv(os.Environ(), env)
		var log boundedLog
		cmd.Stdout, cmd.Stderr = &log, &log
		err := cmd.Run()
		if err == nil {
			return nil
		}
		message := strings.TrimSpace(string(log.data))
		// Upstream has kernel-held writer/index leases. Its initial namespace
		// creation and permanent-lock publication can race another init.
		// Re-observe only those transient conditions; never delete locks or data.
		transient := strings.Contains(message, "namespace already exists") || strings.Contains(message, "permanent index lock creation lost a race")
		if attempt >= 10 || !transient {
			return fmt.Errorf("initialize CodeGraph: %w: %s", err, message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Start schedules initialization immediately without blocking project opening.
// It is called only after the App has been successfully constructed.
func (c *CodeGraphClient) Start() { c.once.Do(func() { go c.start() }) }
func (c *CodeGraphClient) start() {
	defer close(c.done)
	err := c.connect()
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}
func (c *CodeGraphClient) connect() error {
	if strings.TrimSpace(c.root) == "" {
		return fmt.Errorf("CodeGraph requires a project work directory")
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	home, _ = filepath.EvalSymlinks(home)
	if root == filepath.Dir(root) || root == home {
		return fmt.Errorf("CodeGraph refuses filesystem root or home directory")
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("CodeGraph project is not a directory")
	}
	r, err := c.resolve(c.ctx, c.runtimeDir, c.cache)
	if err != nil {
		return err
	}
	env := codeGraphEnv()
	if err := c.initialize(c.ctx, r, root, env); err != nil {
		return err
	}
	args := []string{"serve", "--mcp", "--path", root}
	client := c.factory(config.MCPServerConfig{Name: "solcode-codegraph", Transport: "stdio", Command: r.Binary, Args: args, Env: env})
	if client == nil {
		return fmt.Errorf("CodeGraph client factory returned nil")
	}
	if stdio, ok := client.(*stdioClient); ok {
		stdio.workDir = root
	}
	// Pass a session lifetime, not the deadline of the first query, to stdio.
	timer := time.AfterFunc(30*time.Second, c.cancel)
	err = client.Start(c.ctx)
	timer.Stop()
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("start CodeGraph MCP: %w", err)
	}
	// Upstream can answer a concurrent warm-start handshake in read-only
	// fallback mode before its daemon is attachable. Its explicit-project broker
	// establishes a session-owned shared-daemon lease and one catch-up. Prime
	// that broker here, still in the background, rather than reporting ready
	// merely because ordinary snapshot queries work. This path is pinned by us,
	// never accepted from model input. No local scanner/watcher is needed.
	primeCtx, primeCancel := context.WithTimeout(c.ctx, 2*time.Minute)
	primeArgs, _ := json.Marshal(map[string]string{"projectPath": root})
	var primed *tool.ContentBlock
	if stdio, ok := client.(*stdioClient); ok {
		primed, err = stdio.callTool(primeCtx, "codegraph_status", primeArgs)
	} else {
		primed, err = client.CallTool(primeCtx, "codegraph_status", primeArgs)
	}
	primeCancel()
	if err != nil || primed == nil || primed.IsError {
		_ = client.Close()
		if err != nil {
			return fmt.Errorf("prepare CodeGraph live synchronization: %w", err)
		}
		return fmt.Errorf("prepare CodeGraph live synchronization: %v", primed)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		_ = client.Close()
		return c.ctx.Err()
	}
	c.client = client
	return nil
}

func (c *CodeGraphClient) CallTool(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
	// Defense in depth: callers cannot route a management tool or change project.
	allowed := map[string]bool{"codegraph_explore": true, "codegraph_node": true, "codegraph_search": true, "codegraph_callers": true, "codegraph_callees": true, "codegraph_impact": true, "codegraph_files": true, "codegraph_status": true}
	if !allowed[name] {
		return tool.ErrorResult("unsupported CodeGraph query"), nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, err
	}
	if _, ok := args["projectPath"]; ok {
		return tool.ErrorResult("CodeGraph project override is not allowed"), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
	default:
		return tool.Result("CodeGraph is initializing in the background. Use Grep, View or LSP for this turn; do not poll status repeatedly."), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return tool.ErrorResult("CodeGraph runtime is closed"), nil
	}
	if c.err != nil {
		return tool.Result("CodeGraph unavailable: " + c.err.Error() + ". Use Grep, View or LSP."), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var result *tool.ContentBlock
	var err error
	if stdio, ok := c.client.(*stdioClient); ok {
		result, err = stdio.callTool(ctx, name, input)
	} else {
		result, err = c.client.CallTool(ctx, name, input)
	}
	if err != nil && ctx.Err() == nil {
		c.err = fmt.Errorf("CodeGraph transport failed: %w", err)
		_ = c.client.Close()
		c.cancel()
		c.client = nil
		return tool.Result(c.err.Error() + ". Use Grep, View or LSP; reopen the project to restart the proxy."), nil
	}
	return result, err
}
func (c *CodeGraphClient) Close() error {
	// Never launch a configured-but-not-started runtime just to close it.
	c.once.Do(func() { c.cancel(); close(c.done) })
	interrupted := false
	select {
	case <-c.done:
		// Ready proxy: close stdin gracefully before canceling its process.
	default:
		interrupted = true
		c.cancel() // Interrupt resolution/initialization/handshake, then join.
		<-c.done
	}
	defer c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		if interrupted {
			return nil
		} // Expected process cancellation during startup.
		return err
	}
	return nil
}
