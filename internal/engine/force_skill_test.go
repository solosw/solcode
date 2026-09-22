package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
)

// skillRegistryWith builds a registry backed by real SKILL.md files on disk, so
// the force-load path is exercised against the same loader the app uses.
func skillRegistryWith(t *testing.T, skills map[string]string) *skill.Registry {
	t.Helper()
	dir := t.TempDir()
	for name, body := range skills {
		root := filepath.Join(dir, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return skill.LoadFromDirs(dir)
}

const verifySkillBody = `---
name: verify
description: Run the real build and tests.
allowed-tools: Bash View
---

# verify

1. Establish the baseline.
2. Build.
3. Run the narrowest test.
`

const exploreSkillBody = `---
name: explore
description: Read-only reconnaissance.
---

# explore

Map the code before changing it.
`

func twoSkillRegistry(t *testing.T) *skill.Registry {
	t.Helper()
	return skillRegistryWith(t, map[string]string{
		"verify":  verifySkillBody,
		"explore": exploreSkillBody,
	})
}

// A Jev-selected skill must be loaded into the conversation, not merely
// advertised. Advertising alone leaves the model free to ignore the selection,
// which would make the routing decision worthless.
func TestForceLoadedSkillRendersChosenSkill(t *testing.T) {
	registry := twoSkillRegistry(t)
	selected := []SkillInfo{{Name: "verify", Description: "Run the real build and tests."}}
	eng := &Engine{config: Config{
		Router:        &Router{},
		SkillRegistry: registry,
		Skills:        selected,
	}}

	got := eng.forceLoadedSkill(context.Background(), "check my change", selected)
	if got == "" {
		t.Fatal("expected the selected skill to be force-loaded")
	}
	// The rendered text must carry the skill identity, its instructions, and its
	// root, exactly as the Skill tool would have returned.
	for _, want := range []string{"[Skill: verify]", "# verify", "Establish the baseline", "Root:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("force-loaded text missing %q:\n%s", want, got)
		}
	}
	// It must not leak the skill that was not selected.
	if strings.Contains(got, "[Skill: explore]") {
		t.Fatal("force-loaded text should contain only the selected skill")
	}
}

// The "none" answer is an explicit hand-back: no skill is force-loaded and no
// catalog is narrowed, so the model decides for itself.
func TestNoneSkillIsNotForceLoaded(t *testing.T) {
	registry := twoSkillRegistry(t)

	// Router declines, so routedSkills returns the full catalog (2 entries) and
	// forceLoadedSkill must produce nothing.
	stub := &routerStub{
		choice:        RouterNoneSkill,
		confidence:    0.95,
		probabilities: map[string]float64{RouterNoneSkill: 0.95, "verify": 0.05},
	}
	skills := []SkillInfo{
		{Name: "verify", Description: "Run the real build and tests."},
		{Name: "explore", Description: "Read-only reconnaissance."},
	}
	eng := &Engine{config: Config{
		Router:        stub.router(t),
		SkillRegistry: registry,
		Skills:        skills,
	}}

	routed := eng.routedSkills(context.Background(), "something unrelated")
	// The catalog stays complete so the model can still choose.
	if len(routed) != 2 {
		t.Fatalf("routed = %#v, want the full catalog on a none answer", routed)
	}
	if got := eng.forceLoadedSkill(context.Background(), "something unrelated", routed); got != "" {
		t.Fatalf("force-loaded = %q, want nothing for a none answer", got)
	}
}

// A single-entry catalog can legitimately be the "none" sentinel itself; it must
// never resolve to a real skill.
func TestForceLoadedSkillRejectsNoneSentinel(t *testing.T) {
	registry := twoSkillRegistry(t)
	eng := &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}}
	if got := eng.forceLoadedSkill(context.Background(), "p", []SkillInfo{{Name: RouterNoneSkill}}); got != "" {
		t.Fatalf("force-loaded = %q, want empty", got)
	}
}

// Every path that cannot resolve a skill must return nothing rather than a
// partial or invented activation.
func TestForceLoadedSkillFallbacksReturnNothing(t *testing.T) {
	registry := twoSkillRegistry(t)
	fullCatalog := []SkillInfo{
		{Name: "verify", Description: "v"},
		{Name: "explore", Description: "e"},
	}

	cases := []struct {
		name   string
		eng    *Engine
		skills []SkillInfo
	}{
		{
			name:   "no router",
			eng:    &Engine{config: Config{SkillRegistry: registry}},
			skills: fullCatalog,
		},
		{
			name:   "no skill registry",
			eng:    &Engine{config: Config{Router: &Router{}}},
			skills: fullCatalog,
		},
		{
			name:   "full catalog is not a selection",
			eng:    &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}},
			skills: fullCatalog,
		},
		{
			name:   "no skills at all",
			eng:    &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}},
			skills: nil,
		},
		{
			name:   "selected name is not in the registry",
			eng:    &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}},
			skills: []SkillInfo{{Name: "does-not-exist"}},
		},
		{
			name:   "blank name",
			eng:    &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}},
			skills: []SkillInfo{{Name: "   "}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.eng.forceLoadedSkill(context.Background(), "prompt", tc.skills); got != "" {
				t.Fatalf("force-loaded = %q, want empty", got)
			}
		})
	}
}

// A force-loaded skill must reach the model's message stream, and must be framed
// so the model applies it instead of calling the Skill tool again.
func TestForceSkillReachesMessages(t *testing.T) {
	registry := twoSkillRegistry(t)
	selected := []SkillInfo{{Name: "verify", Description: "Run the real build and tests."}}
	eng := &Engine{config: Config{Router: &Router{}, SkillRegistry: registry, Skills: selected}}
	activation := eng.forceLoadedSkill(context.Background(), "check my change", selected)
	if activation == "" {
		t.Fatal("expected an activation")
	}

	builder := ContextBuilder{Skills: selected, ForceSkill: activation}
	req := builder.Build(BuildRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("check my change"))},
	})

	found := false
	for _, message := range req.Messages {
		text := messageText(message)
		if strings.Contains(text, "Selected skill for this request") && strings.Contains(text, "[Skill: verify]") {
			found = true
			// The framing must tell the model the skill is already loaded, so it
			// does not waste a turn calling the Skill tool.
			if !strings.Contains(text, "do not call the Skill tool") {
				t.Fatalf("activation framing is missing the already-loaded note:\n%s", text)
			}
			// It must also allow disagreement, so a wrong selection is reported
			// rather than obeyed blindly.
			if !strings.Contains(text, "not to fit") {
				t.Fatalf("activation framing should permit disagreement:\n%s", text)
			}
		}
	}
	if !found {
		t.Fatalf("force-loaded skill did not reach the message stream")
	}
}

// With no force-loaded skill the block must be absent, so ordinary turns keep
// their exact previous prompt shape.
func TestNoForceSkillLeavesMessagesUnchanged(t *testing.T) {
	messages := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))}
	builder := ContextBuilder{}
	req := builder.Build(BuildRequest{Messages: messages})
	for _, message := range req.Messages {
		if strings.Contains(messageText(message), "Selected skill for this request") {
			t.Fatal("no force-loaded skill should mean no skill block")
		}
	}
}

// The activation must be byte-identical to what the Skill tool returns, because
// both paths have to resolve a skill's Root and bundled files the same way.
func TestForceLoadedActivationMatchesSkillToolRendering(t *testing.T) {
	registry := twoSkillRegistry(t)
	def, ok := registry.Find("verify")
	if !ok {
		t.Fatal("verify not loaded")
	}
	eng := &Engine{config: Config{Router: &Router{}, SkillRegistry: registry}}
	got := eng.forceLoadedSkill(context.Background(), "p", []SkillInfo{{Name: "verify"}})
	if want := tool.RenderSkillActivation(def, ""); got != want {
		t.Fatalf("force-loaded activation differs from Skill tool rendering:\n got=%q\nwant=%q", got, want)
	}
}

func messageText(message sdk.MessageParam) string {
	var b strings.Builder
	for _, block := range message.Content {
		if block.OfText != nil {
			b.WriteString(block.OfText.Text)
		}
	}
	return b.String()
}
