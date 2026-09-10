package workflowui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

// Config configures the local workflow node editor server.
type Config struct {
	WorkDir     string
	UserDir     string
	ProjectDir  string
	List        func() []workflow.Definition
	Save        func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error)
	Delete      func(name string) error
	Reload      func()
	Tools       func() []string
	Addr        string // empty -> 127.0.0.1:0
	OpenBrowser bool

	// Settings support. Settings returns the current config; ApplySettings
	// receives a (possibly partial) config update and applies it at runtime
	// plus persists it. Skills returns skill descriptors for the UI.
	Settings      func() config.Config
	ApplySettings func(next config.Config) error
	SaveMCPServer func(server config.MCPServerConfig, scope string) error
	Skills        func() []SkillInfo
}

// SkillInfo is a skill descriptor exposed to the settings UI.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// Server is a local HTTP server hosting the node editor UI.
type Server struct {
	cfg    Config
	server *http.Server
	url    string
}

type workflowDTO struct {
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	ExecutionMode string              `json:"execution_mode"`
	Tasks         []workflow.TaskSpec `json:"tasks"`
	Path          string              `json:"path,omitempty"`
	Source        string              `json:"source,omitempty"`
	Layout        workflow.Layout     `json:"layout"`
}

type saveRequest struct {
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	ExecutionMode string              `json:"execution_mode"`
	Tasks         []workflow.TaskSpec `json:"tasks"`
	Scope         string              `json:"scope"`
	Layout        *workflow.Layout    `json:"layout,omitempty"`
}

type metaResponse struct {
	WorkDir    string   `json:"work_dir"`
	UserDir    string   `json:"user_dir"`
	ProjectDir string   `json:"project_dir"`
	Tools      []string `json:"tools"`
}

// Start launches the editor server and optionally opens a browser.
func Start(cfg Config) (*Server, string, error) {
	if cfg.List == nil || cfg.Save == nil {
		return nil, "", fmt.Errorf("workflowui: List and Save callbacks are required")
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("workflowui listen: %w", err)
	}
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/meta", s.handleMeta)
	mux.HandleFunc("/api/workflows", s.handleWorkflows)
	mux.HandleFunc("/api/workflows/", s.handleWorkflowByName)
	mux.HandleFunc("/api/save", s.handleSave)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/providers", s.handleProviders)
	mux.HandleFunc("/api/providers/", s.handleProviderByName)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/", s.handleModelByName)
	mux.HandleFunc("/api/mcp-servers", s.handleMCPServers)
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		_ = ln.Close()
		return nil, "", err
	}
	mux.Handle("/", http.FileServer(http.FS(staticRoot)))

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	url := "http://" + ln.Addr().String() + "/"
	s.url = url

	go func() {
		_ = s.server.Serve(ln)
	}()

	if cfg.OpenBrowser {
		_ = openBrowser(url)
	}
	return s, url, nil
}

// URL returns the base URL of the running server.
func (s *Server) URL() string {
	if s == nil {
		return ""
	}
	return s.url
}

// Close shuts down the server.
func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, metaResponse{
		WorkDir:    s.cfg.WorkDir,
		UserDir:    s.cfg.UserDir,
		ProjectDir: s.cfg.ProjectDir,
		Tools:      s.availableTools(),
	})
}

func (s *Server) availableTools() []string {
	if s.cfg.Tools != nil {
		if tools := uniqueSorted(s.cfg.Tools()); len(tools) > 0 {
			return tools
		}
	}
	return defaultEditorTools()
}

func defaultEditorTools() []string {
	return []string{
		"AskUser", "Bash", "Diff", "Edit", "MultiEdit", "MultiWrite", "Fetch", "Glob", "Grep",
		"LS", "LSP", "Patch", "Skill", "Task", "TodoWrite",
		"View", "ViewImage", "WebSearch", "Write",
	}
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defs := s.cfg.List()
	out := make([]workflowDTO, 0, len(defs))
	for _, def := range defs {
		out = append(out, toDTO(def))
	}
	writeJSON(w, out)
}

func (s *Server) handleWorkflowByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/workflows/")
	name = strings.Trim(name, "/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		for _, def := range s.cfg.List() {
			if def.Name == name {
				writeJSON(w, toDTO(def))
				return
			}
		}
		http.Error(w, "workflow not found", http.StatusNotFound)
	case http.MethodDelete:
		if s.cfg.Delete == nil {
			http.Error(w, "delete not configured", http.StatusNotImplemented)
			return
		}
		if err := s.cfg.Delete(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.cfg.Reload != nil {
			s.cfg.Reload()
		}
		writeJSON(w, map[string]any{"ok": true, "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req saveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	scope := workflow.SaveScope(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = workflow.SaveScopeProject
	}
	def := workflow.Definition{
		Name:          req.Name,
		Description:   req.Description,
		ExecutionMode: req.ExecutionMode,
		Tasks:         req.Tasks,
	}
	path, err := s.cfg.Save(def, scope, req.Layout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.Reload != nil {
		s.cfg.Reload()
	}
	writeJSON(w, map[string]any{
		"ok":   true,
		"path": path,
		"name": def.Name,
	})
}

// ---- Settings ----

type settingsResponse struct {
	Provider   string                   `json:"provider"`
	Model      string                   `json:"model"`
	Effort     string                   `json:"effort"`
	MaxTurns   int                      `json:"max_turns"`
	MaxContext int64                    `json:"max_context_tokens"`
	APIFormat  string                   `json:"api_format"`
	Providers  []providerSummary        `json:"providers"`
	MCPServers []config.MCPServerConfig `json:"mcp_servers"`
	Skills     []SkillInfo              `json:"skills"`
}

type providerSummary struct {
	Name      string   `json:"name"`
	APIKeySet bool     `json:"api_key_set"`
	BaseURL   string   `json:"base_url"`
	APIFormat string   `json:"api_format"`
	Models    []string `json:"models"`
}

type mcpServerRequest struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Env       map[string]string `json:"env"`
	Scope     string            `json:"scope"`
}

func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}

	var req mcpServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	transport := config.NormalizeMCPTransport(req.Transport)
	if name == "" {
		http.Error(w, "MCP server name required", http.StatusBadRequest)
		return
	}
	switch transport {
	case "stdio":
		if strings.TrimSpace(req.Command) == "" {
			http.Error(w, "stdio MCP server command required", http.StatusBadRequest)
			return
		}
	case config.MCPTransportStreamableHTTP:
		if strings.TrimSpace(req.URL) == "" {
			http.Error(w, transport+" MCP server URL required", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "MCP transport must be stdio or STREAMABLE_HTTP (http/sse accepted as aliases)", http.StatusBadRequest)
		return
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope != "global" && scope != "project" {
		http.Error(w, "MCP scope must be global or project", http.StatusBadRequest)
		return
	}

	next := s.cfg.Settings()
	for _, server := range next.MCP.Servers {
		if strings.EqualFold(server.Name, name) {
			http.Error(w, "MCP server already exists: "+name, http.StatusConflict)
			return
		}
	}
	server := config.MCPServerConfig{
		Name:      name,
		Transport: transport,
		Type:      transport,
		Command:   strings.TrimSpace(req.Command),
		Args:      cleanStrings(req.Args),
		URL:       strings.TrimSpace(req.URL),
		Headers:   cleanStringMap(req.Headers),
		Env:       cleanStringMap(req.Env),
	}
	if s.cfg.SaveMCPServer != nil {
		if err := s.cfg.SaveMCPServer(server, scope); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		next.MCP.Servers = append(next.MCP.Servers, server)
		next.MCPServers = cloneMCPServers(next.MCP.Servers)
		if err := s.cfg.ApplySettings(next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "name": name, "scope": scope})
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cleanStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.getSettings(w, r)
		return
	}
	if r.Method == http.MethodPost {
		s.postSettings(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	cfg := s.cfg.Settings()
	resp := settingsResponse{
		Provider:   cfg.Provider,
		Model:      settingsModelName(cfg),
		Effort:     cfg.Effort,
		MaxTurns:   cfg.MaxTurns,
		MaxContext: cfg.MaxContextTokens,
		APIFormat:  cfg.APIFormat,
		MCPServers: cfg.MCP.Servers,
	}
	for _, p := range cfg.Providers {
		summary := providerSummary{
			Name:      p.Name,
			APIKeySet: strings.TrimSpace(p.APIKey) != "" || strings.TrimSpace(p.APIKeyEnv) != "",
			BaseURL:   p.BaseURL,
			APIFormat: p.APIFormat,
		}
		for _, m := range p.Models {
			summary.Models = append(summary.Models, m.Name)
		}
		resp.Providers = append(resp.Providers, summary)
	}
	if s.cfg.Skills != nil {
		resp.Skills = s.cfg.Skills()
	}
	writeJSON(w, resp)
}

func settingsModelName(cfg config.Config) string {
	for _, p := range cfg.Providers {
		if p.Name != cfg.Provider {
			continue
		}
		for _, m := range p.Models {
			if m.Name == cfg.Model || m.ID == cfg.Model {
				return m.Name
			}
		}
	}
	return cfg.Model
}

type settingsUpdate struct {
	Provider   *string `json:"provider,omitempty"`
	Model      *string `json:"model,omitempty"`
	Effort     *string `json:"effort,omitempty"`
	MaxTurns   *int    `json:"max_turns,omitempty"`
	MaxContext *int64  `json:"max_context_tokens,omitempty"`
	// MCP server Disabled toggles keyed by name.
	MCPDisabled map[string]bool `json:"mcp_disabled,omitempty"`
	// Skill enable/disable toggles keyed by name.
	SkillsEnabled  map[string]bool `json:"skills_enabled,omitempty"`
	SkillsDisabled map[string]bool `json:"skills_disabled,omitempty"`
}

func (s *Server) postSettings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	var req settingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	next := s.cfg.Settings()

	if req.Provider != nil {
		next.Provider = strings.TrimSpace(*req.Provider)
	}
	if req.Model != nil {
		next.Model = strings.TrimSpace(*req.Model)
	}
	if req.Effort != nil {
		next.Effort = strings.TrimSpace(*req.Effort)
	}
	if req.MaxTurns != nil {
		next.MaxTurns = *req.MaxTurns
	}
	if req.MaxContext != nil {
		next.MaxContextTokens = *req.MaxContext
	}
	applyActiveModelSettings(&next, req)

	// MCP toggles
	if len(req.MCPDisabled) > 0 {
		for i := range next.MCP.Servers {
			if disabled, ok := req.MCPDisabled[next.MCP.Servers[i].Name]; ok {
				next.MCP.Servers[i].Disabled = disabled
			}
		}
		next.MCPServers = cloneMCPServers(next.MCP.Servers)
	}

	// Skill toggles
	if len(req.SkillsEnabled) > 0 || len(req.SkillsDisabled) > 0 {
		for name, enabled := range req.SkillsEnabled {
			if enabled {
				next.Skills.Disabled = removeString(next.Skills.Disabled, name)
				if len(next.Skills.Enabled) > 0 {
					next.Skills.Enabled = appendUnique(next.Skills.Enabled, name)
				}
			}
		}
		for name, disabled := range req.SkillsDisabled {
			if disabled {
				next.Skills.Enabled = removeString(next.Skills.Enabled, name)
				next.Skills.Disabled = appendUnique(next.Skills.Disabled, name)
			}
		}
	}

	// Note: we intentionally do NOT call Normalize() here. The ApplySettings
	// callback handles persistence + reload, which normalizes the full config.
	// Calling Normalize on the partial copy would fail when the provider/model
	// configuration is in an intermediate state (e.g. a new provider with no
	// models yet), blocking legitimate saves.
	if err := s.cfg.ApplySettings(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func applyActiveModelSettings(cfg *config.Config, req settingsUpdate) {
	if cfg == nil {
		return
	}
	for providerIndex := range cfg.Providers {
		provider := &cfg.Providers[providerIndex]
		if provider.Name != cfg.Provider {
			continue
		}
		for modelIndex := range provider.Models {
			model := &provider.Models[modelIndex]
			if model.Name != cfg.Model && model.ID != cfg.Model {
				continue
			}
			if req.MaxContext != nil {
				model.MaxContextTokens = *req.MaxContext
			}
			if req.MaxTurns != nil {
				maxTurns := *req.MaxTurns
				model.MaxTurns = &maxTurns
			}
			if req.Effort != nil {
				model.Effort = strings.TrimSpace(*req.Effort)
			}
			return
		}
	}
}

// ---- Providers add/delete ----

type providerRequest struct {
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
	APIFormat string `json:"api_format"`
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	var req providerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "provider name required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		http.Error(w, "base_url required", http.StatusBadRequest)
		return
	}
	next := s.cfg.Settings()
	for _, p := range next.Providers {
		if p.Name == name {
			http.Error(w, fmt.Sprintf("provider %q already exists", name), http.StatusConflict)
			return
		}
	}
	apiFormat := strings.TrimSpace(req.APIFormat)
	if apiFormat == "" {
		apiFormat = "anthropic"
	}
	next.Providers = append(append([]config.ProviderConfig(nil), next.Providers...), config.ProviderConfig{
		Name:      name,
		APIKey:    strings.TrimSpace(req.APIKey),
		BaseURL:   strings.TrimSpace(req.BaseURL),
		APIFormat: apiFormat,
		Models:    []config.ModelConfig{},
	})
	if err := s.cfg.ApplySettings(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

func (s *Server) handleProviderByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	name = strings.Trim(name, "/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	next := s.cfg.Settings()
	found := false
	out := make([]config.ProviderConfig, 0, len(next.Providers))
	for _, p := range next.Providers {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		http.Error(w, fmt.Sprintf("provider %q not found", name), http.StatusNotFound)
		return
	}
	next.Providers = out
	// Clear active provider/model if they belonged to the deleted provider.
	if next.Provider == name {
		next.Provider = ""
	}
	modelBelongsToProvider := false
	for _, p := range next.Providers {
		for _, m := range p.Models {
			if m.Name == next.Model || m.ID == next.Model {
				modelBelongsToProvider = true
				break
			}
		}
	}
	if !modelBelongsToProvider {
		next.Model = ""
	}
	if err := s.cfg.ApplySettings(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

// ---- Models add/delete ----

type modelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	var req modelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	providerName := strings.TrimSpace(req.Provider)
	modelID := strings.TrimSpace(req.Model)
	if providerName == "" || modelID == "" {
		http.Error(w, "provider and model required", http.StatusBadRequest)
		return
	}
	next := s.cfg.Settings()
	found := false
	for i := range next.Providers {
		p := &next.Providers[i]
		if p.Name != providerName {
			continue
		}
		found = true
		for _, m := range p.Models {
			if m.Name == modelID || m.ID == modelID {
				http.Error(w, fmt.Sprintf("model %q already exists for provider %q", modelID, providerName), http.StatusConflict)
				return
			}
		}
		p.Models = append(p.Models, config.ModelConfig{
			Name:     modelID,
			ID:       modelID,
			Provider: providerName,
		})
		break
	}
	if !found {
		http.Error(w, fmt.Sprintf("provider %q not found", providerName), http.StatusNotFound)
		return
	}
	if err := s.cfg.ApplySettings(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "provider": providerName, "model": modelID})
}

func (s *Server) handleModelByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Settings == nil || s.cfg.ApplySettings == nil {
		http.Error(w, "settings not configured", http.StatusNotImplemented)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/models/")
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	if len(parts) != 2 {
		http.Error(w, "path format: /api/models/{provider}/{model}", http.StatusBadRequest)
		return
	}
	providerName := parts[0]
	modelID := parts[1]
	if providerName == "" || modelID == "" {
		http.Error(w, "provider and model required", http.StatusBadRequest)
		return
	}
	next := s.cfg.Settings()
	found := false
	for i := range next.Providers {
		p := &next.Providers[i]
		if p.Name != providerName {
			continue
		}
		out := make([]config.ModelConfig, 0, len(p.Models))
		for _, m := range p.Models {
			if m.Name == modelID || m.ID == modelID {
				found = true
				continue
			}
			out = append(out, m)
		}
		p.Models = out
		break
	}
	if !found {
		http.Error(w, fmt.Sprintf("model %q not found for provider %q", modelID, providerName), http.StatusNotFound)
		return
	}
	if next.Model == modelID {
		next.Model = ""
	}
	if err := next.Normalize(); err != nil {
		http.Error(w, "normalize: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.ApplySettings(next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "provider": providerName, "model": modelID})
}

// ---- helpers ----

func toDTO(def workflow.Definition) workflowDTO {
	layout := workflow.Layout{Nodes: map[string]workflow.NodePos{}}
	if def.Path != "" {
		if loaded, err := workflow.LoadLayout(def.Path); err == nil {
			layout = loaded
		}
	}
	if len(layout.Nodes) == 0 {
		layout = workflow.DefaultLayout(def)
	}
	return workflowDTO{
		Name:          def.Name,
		Description:   def.Description,
		ExecutionMode: def.ExecutionMode,
		Tasks:         def.Tasks,
		Path:          def.Path,
		Source:        def.Source,
		Layout:        layout,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// OpenBrowser opens url with the platform default browser.
func OpenBrowser(url string) error {
	return openBrowser(url)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func cloneMCPServers(servers []config.MCPServerConfig) []config.MCPServerConfig {
	out := make([]config.MCPServerConfig, len(servers))
	for i, s := range servers {
		out[i] = s
	}
	return out
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}

func removeString(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}
