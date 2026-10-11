package workflowui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

func TestServerListAndSave(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project-workflows")
	userDir := filepath.Join(dir, "user-workflows")

	var saved []workflow.Definition
	srv, url, err := Start(Config{
		WorkDir:    dir,
		UserDir:    userDir,
		ProjectDir: projectDir,
		List: func() []workflow.Definition {
			return saved
		},
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			path, err := workflow.SaveToDirWithLayout(def, projectDir, layout)
			if err != nil {
				return "", err
			}
			def.Path = path
			// replace or append
			found := false
			for i := range saved {
				if saved[i].Name == def.Name {
					saved[i] = def
					found = true
					break
				}
			}
			if !found {
				saved = append(saved, def)
			}
			return path, nil
		},
		Delete: func(name string) error {
			for i, def := range saved {
				if def.Name == name {
					if err := workflow.Delete(def, []string{projectDir, userDir}); err != nil {
						return err
					}
					saved = append(saved[:i], saved[i+1:]...)
					return nil
				}
			}
			return fmt.Errorf("workflow not found")
		},
		OpenBrowser: false,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	res, err := http.Get(url + "api/meta")
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("meta status %d", res.StatusCode)
	}
	var meta map[string]any
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	tools, _ := meta["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("expected tools in meta, got %#v", meta)
	}

	body := `{
	  "name": "node-demo",
	  "description": "from web",
	  "execution_mode": "auto",
	  "scope": "project",
	  "tasks": [
	    {"id":"a","description":"A","prompt":"do a","difficulty":"easy"},
	    {"id":"b","description":"B","prompt":"do b","depends_on":["a"]}
	  ],
	  "layout": {"nodes":{"a":{"x":10,"y":20},"b":{"x":200,"y":20}}}
	}`
	resp, err := http.Post(url+"api/save", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("save status %d: %s", resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if out["path"] == nil || out["path"] == "" {
		t.Fatalf("missing path: %#v", out)
	}

	listResp, err := http.Get(url + "api/workflows")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listResp.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0]["name"] != "node-demo" {
		t.Fatalf("list = %#v", list)
	}

	// static index
	indexResp, err := http.Get(url)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer indexResp.Body.Close()
	raw, _ := io.ReadAll(indexResp.Body)
	if indexResp.StatusCode != 200 || len(raw) < 100 {
		t.Fatalf("index status=%d len=%d", indexResp.StatusCode, len(raw))
	}

	req, err := http.NewRequest(http.MethodDelete, url+"api/workflows/node-demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != 200 {
		body, _ := io.ReadAll(delResp.Body)
		t.Fatalf("delete status %d: %s", delResp.StatusCode, body)
	}
	listResp2, err := http.Get(url + "api/workflows")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp2.Body.Close()
	var list2 []map[string]any
	if err := json.NewDecoder(listResp2.Body).Decode(&list2); err != nil {
		t.Fatal(err)
	}
	if len(list2) != 0 {
		t.Fatalf("expected empty list after delete, got %#v", list2)
	}
}

func TestServerSettingsProvidersModels(t *testing.T) {
	dir := t.TempDir()
	stored := config.Config{
		WorkDir:  dir,
		Provider: "test",
		Model:    "test-model",
		Effort:   "high",
		Providers: []config.ProviderConfig{
			{
				Name:      "test",
				APIKey:    "secret",
				BaseURL:   "https://api.test.com",
				APIFormat: "anthropic",
				Models: []config.ModelConfig{
					{Name: "test-model", ID: "test-model", Provider: "test"},
				},
			},
		},
	}
	stored.MCP.Servers = []config.MCPServerConfig{
		{Name: "fs", Command: "npx", Args: []string{"fs"}, Disabled: false},
	}

	var applied *config.Config
	srv, url, err := Start(Config{
		WorkDir: dir,
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings: func() config.Config { return stored },
		ApplySettings: func(next config.Config) error {
			cp := next
			applied = &cp
			stored = next
			return nil
		},
		Skills: func() []SkillInfo {
			return []SkillInfo{{Name: "review", Description: "code review", Enabled: true}}
		},
		OpenBrowser: false,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// GET /api/settings
	res, err := http.Get(url + "api/settings")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("settings status %d", res.StatusCode)
	}
	var s map[string]any
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	providers, _ := s["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %#v", providers)
	}
	prov0, _ := providers[0].(map[string]any)
	if prov0["name"] != "test" || !prov0["api_key_set"].(bool) {
		t.Fatalf("provider summary wrong: %#v", prov0)
	}
	skills, _ := s["skills"].([]any)
	if len(skills) != 1 || skills[0].(map[string]any)["name"] != "review" {
		t.Fatalf("skills wrong: %#v", skills)
	}

	// POST /api/settings - toggle MCP off
	body := `{"mcp_disabled":{"fs":true}}`
	resp, err := http.Post(url+"api/settings", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("post settings status %d: %s", resp.StatusCode, raw)
	}
	if applied == nil {
		t.Fatalf("settings not applied")
	}
	fsDisabled := false
	for _, s := range applied.MCP.Servers {
		if s.Name == "fs" {
			fsDisabled = s.Disabled
		}
	}
	if !fsDisabled {
		t.Fatalf("MCP fs not disabled: %#v", applied.MCP.Servers)
	}

	// POST /api/providers - add provider
	body = `{"name":"openai","api_key":"sk-xxx","base_url":"https://api.openai.com","api_format":"openai"}`
	resp2, err := http.Post(url+"api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("add provider status %d: %s", resp2.StatusCode, raw)
	}
	if len(applied.Providers) != 2 || applied.Providers[1].Name != "openai" {
		t.Fatalf("provider not added: %#v", applied.Providers)
	}

	// DELETE /api/providers/openai
	req, _ := http.NewRequest(http.MethodDelete, url+"api/providers/openai", nil)
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		raw, _ := io.ReadAll(resp3.Body)
		t.Fatalf("delete provider status %d: %s", resp3.StatusCode, raw)
	}
	if len(applied.Providers) != 1 {
		t.Fatalf("provider not deleted: %#v", applied.Providers)
	}

	// POST /api/models - add model
	body = `{"provider":"test","model":"new-model"}`
	resp4, err := http.Post(url+"api/models", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("add model: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		raw, _ := io.ReadAll(resp4.Body)
		t.Fatalf("add model status %d: %s", resp4.StatusCode, raw)
	}
	if len(applied.Providers[0].Models) != 2 {
		t.Fatalf("model not added: %#v", applied.Providers[0].Models)
	}

	// DELETE /api/models/test/new-model
	req2, _ := http.NewRequest(http.MethodDelete, url+"api/models/test/new-model", nil)
	resp5, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("delete model: %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != 200 {
		raw, _ := io.ReadAll(resp5.Body)
		t.Fatalf("delete model status %d: %s", resp5.StatusCode, raw)
	}
	if len(applied.Providers[0].Models) != 1 {
		t.Fatalf("model not deleted: %#v", applied.Providers[0].Models)
	}

	// Duplicate provider should return 409
	body = `{"name":"test","base_url":"https://api.test.com","api_format":"anthropic"}`
	resp6, err := http.Post(url+"api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("dup provider: %v", err)
	}
	defer resp6.Body.Close()
	if resp6.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate provider, got %d", resp6.StatusCode)
	}
}

func TestServerResponsesProvider(t *testing.T) {
	stored := config.Config{WorkDir: t.TempDir()}
	srv, url, err := Start(Config{
		WorkDir: stored.WorkDir,
		List:    func() []workflow.Definition { return nil },
		Save: func(workflow.Definition, workflow.SaveScope, *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings: func() config.Config { return stored },
		ApplySettings: func(next config.Config) error {
			stored = next
			return nil
		},
		OpenBrowser: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	resp, err := http.Post(url+"api/providers", "application/json", strings.NewReader(
		`{"name":"responses-provider","api_key":"test-key","base_url":"https://api.example.test/v1","api_format":"responses"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(stored.Providers) != 1 || stored.Providers[0].APIFormat != "responses" {
		t.Fatalf("status = %d; providers = %#v", resp.StatusCode, stored.Providers)
	}
	settings, err := http.Get(url + "api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer settings.Body.Close()
	var body struct {
		Providers []struct {
			APIFormat string `json:"api_format"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(settings.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 1 || body.Providers[0].APIFormat != "responses" {
		t.Fatalf("provider summary = %#v", body)
	}
}
