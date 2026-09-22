package app

import (
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/skill/builtin"
)

// jevConfig returns a config with Jev routing enabled or disabled.
func jevConfig(routing bool) config.Config {
	cfg := config.Default()
	cfg.Skills.Paths = nil
	if routing {
		cfg.Jev = config.JevConfig{Enabled: true, APIKey: "k", Routing: true}
	}
	if err := cfg.Normalize(); err != nil {
		panic(err)
	}
	return cfg
}

// The bundled workflow skills exist only in Jev mode. Without Jev the catalog
// is just another list for the chat model to reason about, so it must not be
// registered.
func TestBuiltinSkillsOnlyRegisteredWithJevRouting(t *testing.T) {
	names, err := builtin.BuiltinSkillNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected bundled skills")
	}

	off := loadSkills(jevConfig(false))
	for _, name := range names {
		if _, found := off.Find(name); found {
			t.Fatalf("bundled skill %q must not register without Jev routing", name)
		}
	}

	on := loadSkills(jevConfig(true))
	for _, name := range names {
		if _, found := on.Find(name); !found {
			t.Fatalf("bundled skill %q missing with Jev routing enabled", name)
		}
	}
}

// Enabling Jev without routing leaves skills off: the toggles are independent.
func TestBuiltinSkillsOffWhenJevEnabledWithoutRouting(t *testing.T) {
	cfg := config.Default()
	cfg.Skills.Paths = nil
	cfg.Jev = config.JevConfig{Enabled: true, APIKey: "k", Routing: false}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	registry := loadSkills(cfg)
	if _, found := registry.Find("explore"); found {
		t.Fatal("routing is off, so bundled skills should not register")
	}
}

// Jev routing enabled but keyless means Jev is off, so the bundle stays off too.
func TestBuiltinSkillsOffWithoutAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.Skills.Paths = nil
	cfg.Jev = config.JevConfig{Enabled: true, Routing: true}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.JevEnabled() {
		t.Fatal("test setup: Jev should be disabled without a key")
	}
	registry := loadSkills(cfg)
	if _, found := registry.Find("explore"); found {
		t.Fatal("a keyless Jev must not register bundled skills")
	}
}

// skills.disabled must still win, so a user can opt out of an individual
// bundled skill without turning Jev off.
func TestBuiltinSkillCanBeDisabled(t *testing.T) {
	cfg := jevConfig(true)
	cfg.Skills.Disabled = []string{"explore"}
	registry := loadSkills(cfg)
	if _, found := registry.Find("explore"); found {
		t.Fatal("explore should be disabled")
	}
	if _, found := registry.Find("verify"); !found {
		t.Fatal("verify should still be registered")
	}
}

// An explicit skills.enabled allow-list must be honored for bundled skills too.
func TestBuiltinSkillHonorsEnabledAllowList(t *testing.T) {
	cfg := jevConfig(true)
	cfg.Skills.Enabled = []string{"verify"}
	registry := loadSkills(cfg)
	if _, found := registry.Find("verify"); !found {
		t.Fatal("verify is allow-listed and should be registered")
	}
	if _, found := registry.Find("explore"); found {
		t.Fatal("explore is not allow-listed and should be skipped")
	}
}

// The gate the registration uses must agree with the config's own view of Jev.
func TestJevSkillRoutingGateMatchesJevEnabled(t *testing.T) {
	if jevSkillRoutingEnabled(jevConfig(false)) {
		t.Fatal("routing gate should be false without Jev")
	}
	if !jevSkillRoutingEnabled(jevConfig(true)) {
		t.Fatal("routing gate should be true with Jev routing on")
	}
}

// The bundled skills must land on disk, because the Skill tool resolves
// scripts/references/assets against a real Root.
func TestBuiltinSkillsMaterializeToUserConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	registry := loadSkills(jevConfig(true))
	def, found := registry.Find("verify")
	if !found {
		t.Fatal("verify not registered")
	}
	root := def.Root()
	if root == "" {
		t.Fatal("verify has no root")
	}
	if !strings.Contains(root, "builtin-skills") {
		t.Fatalf("root = %q, want it under builtin-skills", root)
	}
	if !strings.HasSuffix(root, "verify") {
		t.Fatalf("root = %q, want it to end in the skill name", root)
	}
}
