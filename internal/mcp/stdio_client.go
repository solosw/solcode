package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

type stdioClient struct {
	server config.MCPServerConfig

	mu      sync.Mutex
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
}

func NewStdioClient(server config.MCPServerConfig) Client {
	return &stdioClient{server: server}
}

func (c *stdioClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := validateServerConfig(c.server); err != nil {
		return err
	}
	if c.session != nil {
		return nil
	}

	execCmd := exec.CommandContext(ctx, c.server.Command, c.server.Args...)
	// Config env must override parent process env. On Windows env keys are
	// case-insensitive and the first occurrence wins, so simple append is wrong.
	execCmd.Env = mergeProcessEnv(os.Environ(), c.server.Env)
	if c.server.URL != "" {
		return fmt.Errorf("mcp server %q transport stdio does not use url", c.server.Name)
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "solcode", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: execCmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		return fmt.Errorf("connect mcp server %q: %w", c.server.Name, err)
	}

	c.client = client
	c.session = session
	return nil
}

// MergeProcessEnvForTest exports mergeProcessEnv for unit tests.
func MergeProcessEnvForTest(parent []string, extra map[string]string) []string {
	return mergeProcessEnv(parent, extra)
}

// mergeProcessEnv builds a child process environment from the parent env block,
// applying overrides from extra. Existing keys are replaced (case-insensitive on
// Windows). Empty override values still set the key.
func mergeProcessEnv(parent []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return append([]string(nil), parent...)
	}
	caseInsensitive := runtime.GOOS == "windows"
	indexKey := func(key string) string {
		if caseInsensitive {
			return strings.ToLower(key)
		}
		return key
	}

	// Collect override keys (trimmed).
	overrides := make(map[string]string, len(extra))
	overrideIndex := make(map[string]string, len(extra)) // indexKey -> original key
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		overrides[key] = value
		overrideIndex[indexKey(key)] = key
	}
	if len(overrides) == 0 {
		return append([]string(nil), parent...)
	}

	out := make([]string, 0, len(parent)+len(overrides))
	seen := make(map[string]struct{}, len(parent)+len(overrides))
	// Apply parent entries, replacing any key that is overridden.
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			out = append(out, entry)
			continue
		}
		ik := indexKey(key)
		if orig, overridden := overrideIndex[ik]; overridden {
			if _, already := seen[ik]; already {
				continue
			}
			out = append(out, orig+"="+overrides[orig])
			seen[ik] = struct{}{}
			continue
		}
		if _, already := seen[ik]; already {
			continue
		}
		out = append(out, entry)
		seen[ik] = struct{}{}
	}
	// Append overrides that were not present in parent.
	for key, value := range overrides {
		ik := indexKey(key)
		if _, already := seen[ik]; already {
			continue
		}
		out = append(out, key+"="+value)
		seen[ik] = struct{}{}
	}
	return out
}

func (c *stdioClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("mcp server %q is not started", c.server.Name)
	}
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	tools := make([]tool.MCPToolInfo, 0, len(result.Tools))
	for _, sdkTool := range result.Tools {
		tools = append(tools, toolInfoFromSDK(c.server.Name, sdkTool))
	}
	return tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("mcp server %q is not started", c.server.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.ErrorResult("invalid tool input: " + err.Error()), nil
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return contentBlockFromCallResult(result)
}

func (c *stdioClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if c.session != nil {
		err = c.session.Close()
		c.session = nil
	}
	c.client = nil
	return err
}
