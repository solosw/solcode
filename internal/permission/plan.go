package permission

import (
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

// PlanModePromptMarker identifies an already-injected plan-mode instruction block.
const PlanModePromptMarker = "PLAN MODE"

// PlanModeShortInstructions is the compact plan-mode rule set written into the
// STABLE system prompt once, at startup. It is never removed when leaving plan
// mode, so toggling plan<->default does not rewrite the tools+system prefix.
//
// Keep it short: this text is paid for on every cached request. The verbose
// active-mode guidance (role + output format) stays dynamic, delivered via
// ModeInstructions only while plan mode is actually on.
const PlanModeShortInstructions = PlanModePromptMarker + ` (READ-ONLY):
- Produce a plan. Do not create, modify, or delete files, and do not run state-changing commands.
- Allowed anyway: TodoWrite, Task, ModeSwitch, plus read-only tools (View, Grep, Glob, LS, LSP, WebSearch/Fetch).
- Prefer Task sub-agents for broad exploration; cite paths with short evidence instead of dumping file contents.`

// PlanModeMarkerLine starts the dynamic active-mode block. It contains
// PlanModePromptMarker so existing detectors (ephemeral-message stripping,
// legacy cleanup, tests) keep recognizing the block.
const PlanModeMarkerLine = PlanModePromptMarker + " (ACTIVE)"

// PlanModeInstructions is the ACTIVE-mode guidance delivered dynamically while
// plan mode is on (role + required output format). It is not appended to the
// system prompt: the stable prefix already carries the short read-only rules,
// and this detail must stay out of the cached prefix.
const PlanModeInstructions = PlanModeMarkerLine + `
You are a software architect and planning specialist for solcode. Explore the codebase, then design an implementation plan.

Use Task sub-agents for broad or parallel exploration; use View/Grep/Glob/LS/LSP for targeted reads.

=== Required Output Format ===
Markdown, every section in order. Write "None" or "N/A" with a one-line reason when unknown. Do not implement code, and do not dump large file contents — cite paths and short evidence only.

## Goal
1-3 sentences: what will be built / changed and why (user-facing outcome).

## Current State
- Relevant existing components, entry points, and patterns (with file paths)
- Constraints found in the codebase (APIs, config, tests, compatibility)

## Approach
High-level strategy in one short paragraph. Mention 1-2 rejected alternatives only if they matter.

## Architecture & Trade-offs
- Key design decisions, with risks
- Alignment with existing project conventions

## Implementation Plan
Numbered steps in dependency order; each step includes **Files**, **Action**, **Depends on**, and **Done when**.

## Test & Validation Plan
Commands or checks to run after implementation, plus edge cases and regression risks.

## Open Questions
Unresolved product/tech questions that would change the plan (or "None").

## Critical Files for Implementation
Exactly 3-5 repository-relative paths, one per line, no other commentary on those lines.

Produce plans only. Do NOT implement code changes while plan mode is active.`

// PlanModeExtraTools are non-read-only tools explicitly allowed in plan mode.
// Everything else still requires IsReadOnly (or an explicit session allow).
var PlanModeExtraTools = []string{
	tool.TodoWriteToolName,
	tool.TaskToolName,
	tool.ModeSwitchToolName,
}

// IsPlanModeExtraTool reports whether name is allow-listed for plan mode.
func IsPlanModeExtraTool(name string) bool {
	name = strings.TrimSpace(name)
	for _, allowed := range PlanModeExtraTools {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	return false
}

// WrapPlanModePrompt prepends plan-mode instructions to a prompt if missing.
// Prefer AppendPlanModeSystemPrompt for system prompts; this remains for tests
// and any callers that still wrap free-form text.
func WrapPlanModePrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return PlanModeInstructions
	}
	if strings.Contains(prompt, PlanModePromptMarker) {
		return prompt
	}
	return PlanModeInstructions + "\n\n" + prompt
}

// StripPlanModePrompt removes an injected plan-mode instruction block from text.
// Used to clean older sessions and any prompt text that still carries a block.
func StripPlanModePrompt(text string) string {
	if text == "" || !strings.Contains(text, PlanModePromptMarker) {
		return text
	}
	// Prefer stripping the full known instruction block when present.
	if strings.Contains(text, PlanModeInstructions) {
		text = strings.ReplaceAll(text, PlanModeInstructions, "")
	}
	if strings.Contains(text, PlanModeShortInstructions) {
		text = strings.ReplaceAll(text, PlanModeShortInstructions, "")
	}
	if !strings.Contains(text, PlanModePromptMarker) {
		return collapseBlankRuns(text)
	}
	// Fallback: drop from each marker through the end of that paragraph block.
	for {
		idx := strings.Index(text, PlanModePromptMarker)
		if idx < 0 {
			break
		}
		rest := text[idx+len(PlanModePromptMarker):]
		endRel := strings.Index(rest, "\n\n")
		if endRel < 0 {
			text = text[:idx]
			break
		}
		cutEnd := idx + len(PlanModePromptMarker) + endRel
		for cutEnd < len(text) && (text[cutEnd] == '\n' || text[cutEnd] == '\r') {
			cutEnd++
		}
		text = text[:idx] + text[cutEnd:]
	}
	return collapseBlankRuns(text)
}

// AppendPlanModeSystemPrompt appends the compact plan-mode rules to a system
// prompt. Idempotent when the marker is already present.
func AppendPlanModeSystemPrompt(systemPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if strings.Contains(systemPrompt, PlanModePromptMarker) {
		return systemPrompt
	}
	if systemPrompt == "" {
		return PlanModeShortInstructions
	}
	return systemPrompt + "\n\n" + PlanModeShortInstructions
}

func collapseBlankRuns(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}
