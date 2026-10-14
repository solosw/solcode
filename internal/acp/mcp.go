package acp

import (
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/solosw/solcode/internal/config"
)

// parseMCPServers validates ACP transports before configuration normalization,
// which otherwise aliases the unsupported legacy SSE transport to HTTP.
func parseMCPServers(specs []MCPServerSpec) ([]config.MCPServerConfig, error) {
	servers := make([]config.MCPServerConfig, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, fmt.Errorf("MCP server name is required")
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate MCP server name %q", name)
		}
		seen[name] = true
		server := config.MCPServerConfig{
			Name:    name,
			Command: spec.Command,
			Args:    append([]string(nil), spec.Args...),
			Env:     maps.Clone(map[string]string(spec.Env)),
			URL:     strings.TrimSpace(spec.URL),
			Headers: maps.Clone(map[string]string(spec.Headers)),
		}
		switch strings.ToLower(strings.TrimSpace(spec.Type)) {
		case "", "stdio":
			server.Transport = "stdio"
			if strings.TrimSpace(server.Command) == "" {
				return nil, fmt.Errorf("MCP server %q requires command for stdio transport", name)
			}
			if server.URL != "" || len(server.Headers) != 0 {
				return nil, fmt.Errorf("MCP server %q stdio transport must not set url or headers", name)
			}
		case "http":
			server.Transport = config.MCPTransportStreamableHTTP
			endpoint, err := url.Parse(server.URL)
			if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
				return nil, fmt.Errorf("MCP server %q requires an absolute http or https url", name)
			}
			if server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
				return nil, fmt.Errorf("MCP server %q http transport must not set command, args or env", name)
			}
		case "sse":
			return nil, fmt.Errorf("MCP server %q: SSE transport is not supported; use Streamable HTTP (type http)", name)
		default:
			return nil, fmt.Errorf("MCP server %q has unsupported transport type %q", name, spec.Type)
		}
		server.Type = server.Transport
		servers = append(servers, server)
	}
	return servers, nil
}

// mergeMCPServers replaces an entire same-named local definition, rather than
// inheriting local credentials or disabled state into a client definition.
func mergeMCPServers(base, client []config.MCPServerConfig) []config.MCPServerConfig {
	servers := cloneMCPServers(base)
	indices := make(map[string]int, len(servers))
	for i, server := range servers {
		indices[server.Name] = i
	}
	for _, server := range cloneMCPServers(client) {
		if i, ok := indices[server.Name]; ok {
			servers[i] = server
		} else {
			indices[server.Name] = len(servers)
			servers = append(servers, server)
		}
	}
	return servers
}

// persistentMCPServers excludes ACP definitions, retaining any local definition
// shadowed by one so toggling another server cannot save client credentials.
func (sess *acpSession) persistentMCPServers(current []config.MCPServerConfig) []config.MCPServerConfig {
	servers := make([]config.MCPServerConfig, 0, len(current))
	for _, server := range current {
		if !sess.clientMCPNames[server.Name] {
			servers = append(servers, server)
		}
	}
	for _, server := range sess.baseMCPServers {
		if sess.clientMCPNames[server.Name] {
			servers = append(servers, server)
		}
	}
	return cloneMCPServers(servers)
}
