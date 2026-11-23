package engine

import (
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/permission"
)

func TestClientSystemPromptChangesOnlyCoreInstructions(t *testing.T) {
	for _, mode := range []string{"append", "override"} {
		t.Run(mode, func(t *testing.T) {
			builder := ContextBuilder{SystemPrompt: "Configured core preamble.", ProjectRules: "Workspace rules stay.", Skills: []SkillInfo{{Name: "review", Description: "Review the change"}}}
			if mode == "append" {
				builder.SystemPromptAppend = "Client core instructions."
			} else {
				builder.SystemPromptOverride = "Client core instructions."
			}
			got := builder.systemPrompt("/workspace")
			for _, want := range []string{"Client core instructions.", toolUsagePrompt(false), skillsPrompt(builder.Skills), permission.PlanModeShortInstructions, "Workspace rules stay.", "Working directory: /workspace"} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s dropped prompt layer %q", mode, want)
				}
			}
			for _, core := range []string{"Configured core preamble.", defaultSystemPrompt()} {
				if strings.Contains(got, core) != (mode == "append") {
					t.Fatalf("%s did not apply to the authored core", mode)
				}
			}
			if strings.Count(got, "Client core instructions.") != 1 {
				t.Fatal("client instructions duplicated")
			}
			if mode == "append" && strings.Index(got, "Client core instructions.") < strings.Index(got, defaultSystemPrompt()) {
				t.Fatal("append must follow the original core")
			}
			builder.PlanMode = true
			builder.ModeInstructions = planModeSystemPrompt()
			if got != builder.systemPrompt("/workspace") {
				t.Fatal("mode toggle changed the stable system prefix")
			}
		})
	}
}
