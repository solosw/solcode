package app

import (
	"testing"

	"github.com/solosw/solcode/internal/engine"
)

// The engine must receive the skill registry, or a router-selected skill cannot
// be force-loaded and routing would only narrow the catalog — leaving the model
// free to ignore the selection.
func TestEngineConfigReceivesSkillRegistry(t *testing.T) {
	cfg := jevConfig(true)
	registry := loadSkills(cfg)
	if len(registry.All()) == 0 {
		t.Fatal("expected bundled skills with Jev routing enabled")
	}

	ec := engineConfig(cfg, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil)

	if ec.SkillRegistry == nil {
		t.Fatal("engine config must carry the skill registry for force-loading")
	}
	// Every advertised skill must be resolvable by name, because that is the
	// lookup the force-load path performs.
	for _, info := range ec.Skills {
		if _, ok := ec.SkillRegistry.Find(info.Name); !ok {
			t.Fatalf("advertised skill %q is not resolvable in the registry", info.Name)
		}
	}
}

// Without Jev routing the engine gets no bundled skills, so nothing can be
// force-loaded and behavior matches the pre-Jev build.
func TestEngineConfigWithoutJevHasNoBundledSkills(t *testing.T) {
	cfg := jevConfig(false)
	ec := engineConfig(cfg, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil)

	for _, info := range ec.Skills {
		switch info.Name {
		case "explore", "implement", "verify", "research", "debug", "review":
			t.Fatalf("bundled skill %q must not be advertised without Jev routing", info.Name)
		}
	}
}

// Sanity check that the engine type in this package's config is the one the
// router writes into, so the force-load wiring cannot silently drift.
func TestEngineConfigCarriesRouterAndGuardrail(t *testing.T) {
	cfg := jevConfig(true)
	jev, err := buildJev(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ec := engineConfig(cfg, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil)
	ec.Router = jev.router()
	ec.Guardrail = jev.guardrail()

	if ec.Router == nil {
		t.Fatal("expected a router with Jev routing enabled")
	}
	if ec.Guardrail != nil {
		t.Fatal("guardrail should stay off unless its toggle is set")
	}
	// The router type must be the engine's, not a lookalike.
	var _ *engine.Router = ec.Router
}
