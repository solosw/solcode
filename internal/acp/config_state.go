package acp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
)

type configChangeError struct {
	code    int
	message string
}

func (e *configChangeError) Error() string { return e.message }
func invalidConfigError(format string, args ...any) error {
	return &configChangeError{code: JSONRPCInvalidParams, message: fmt.Sprintf(format, args...)}
}
func configErrorCode(err error) int {
	var change *configChangeError
	if errors.As(err, &change) {
		return change.code
	}
	return JSONRPCInternalError
}

// Preflight is local: do not probe a paid model endpoint or claim a provider is
// reachable. Missing credentials and malformed URLs fail before any commit.
func validateModelRuntime(cfg config.Config) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("selected model has no model ID")
	}
	local := false
	if cfg.BaseURL != "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("provider %q has an invalid API endpoint", cfg.Provider)
		}
		ip := net.ParseIP(u.Hostname())
		local = strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	}
	if strings.TrimSpace(cfg.APIKey) == "" && !local {
		return fmt.Errorf("provider %q has no API credentials; configure its API key before selecting this model", cfg.Provider)
	}
	return nil
}

// WithModel/Normalize resolves environment variables and normalizes models in
// place. Copy their backing arrays before preparing a candidate: a rejected
// switch must not mutate another session's provider catalog.
func cloneModelCatalog(cfg config.Config) config.Config {
	cfg.Providers = append([]config.ProviderConfig(nil), cfg.Providers...)
	for i := range cfg.Providers {
		cfg.Providers[i].Models = append([]config.ModelConfig(nil), cfg.Providers[i].Models...)
	}
	return cfg
}

func selectSessionModel(cfg config.Config, provider, model string) (config.Config, error) {
	next := cloneModelCatalog(cfg)
	next.Provider = provider
	if len(next.Providers) > 0 {
		// Re-resolve all endpoint-specific data, not the previous provider's state.
		next.APIKey, next.BaseURL, next.FastModel = "", "", ""
		next.MaxContextTokens, next.MaxTokens = 0, 0
	}
	next, err := next.WithModel(model)
	if err != nil {
		return cfg, err
	}
	next = next.WithModelEffort(cfg.Effort)
	if err := validateModelRuntime(next); err != nil {
		return cfg, err
	}
	return next, nil
}

// Only stable, non-secret identifiers are persisted. This sidecar is separate
// from the transcript so a running prompt cannot overwrite a confirmed config
// with an older in-memory Session during its final save.
type savedSessionConfig struct {
	Model  string          `json:"model"`
	Mode   permission.Mode `json:"mode"`
	Effort string          `json:"effort"`
}

func (sess *acpSession) configStatePath() string {
	if !sess.cfg.Session.Enabled || !sess.cfg.Session.Persist || sess.cfg.Session.Dir == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(sess.persistID()))
	return filepath.Join(sess.cfg.Session.Dir, "acp-config", fmt.Sprintf("%x.json", hash))
}
func (sess *acpSession) saveConfigLocked(next config.Config) error {
	path := sess.configStatePath()
	if path == "" {
		return nil
	}
	_, model := configModelChoices(next)
	return config.SaveLocalOverrides(path, map[string]any{"model": model, "mode": next.PermissionMode, "effort": next.Effort})
}

// commitConfigLocked is shared by set_config_option and legacy set_mode.
// Validate/compute before this function. A disk error leaves runtime state and
// the last confirmed config untouched. SwitchModel has no network side effects.
func (sess *acpSession) commitConfigLocked(next config.Config) error {
	if sess.application == nil || sess.application.Permissions == nil || sess.application.Engine == nil {
		return fmt.Errorf("session runtime is not ready")
	}
	if err := sess.saveConfigLocked(next); err != nil {
		return fmt.Errorf("could not save session configuration: %w", err)
	}
	if sess.prompting {
		sess.application.UpdateModelForNextCall(next)
		sess.pendingConfig = true
	} else {
		if err := sess.application.SwitchModel(next); err != nil {
			return err
		}
		sess.pendingConfig = false
	}
	sess.application.Permissions.SetMode(next.PermissionMode)
	sess.cfg = next
	return nil
}

func (sess *acpSession) restoreConfig(ctx context.Context) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	path := sess.configStatePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read session configuration: %w", err)
	}
	var saved savedSessionConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("invalid saved session configuration: %w", err)
	}
	next := sess.cfg
	choices, _ := configModelChoices(next)
	found := false
	for _, choice := range choices {
		if choice.Value != saved.Model {
			continue
		}
		candidate, selectErr := selectSessionModel(next, choice.provider, choice.model)
		err = selectErr
		if err == nil {
			next, found = candidate, true
		}
		break
	}
	if !found {
		sess.configNotices = append(sess.configNotices, "Saved model is unavailable; using the current configured default model.")
	}
	if permission.IsSupportedMode(saved.Mode) {
		next.PermissionMode = permission.NormalizeMode(saved.Mode)
		next.Permissions.Mode = next.PermissionMode
	} else {
		sess.configNotices = append(sess.configNotices, "Saved agent mode is unavailable; using the current default mode.")
	}
	next = next.WithModelEffort(saved.Effort)
	if saved.Effort != next.Effort {
		sess.configNotices = append(sess.configNotices, "Saved thinking level is unavailable for this model; using its valid default.")
	}
	if err := validateModelRuntime(next); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sess.commitConfigLocked(next); err != nil {
		return err
	}
	return nil
}
