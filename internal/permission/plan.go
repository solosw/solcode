package permission

import (
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

// PlanModePromptMarker identifies an already-injected plan-mode instruction block.
const PlanModePromptMarker = "=== PLAN MODE (READ-ONLY) ==="

// PlanModeExtraTools are non-read-only tools explicitly allowed in plan mode.
// Everything else still requires IsReadOnly (or an explicit session allow).
var PlanModeExtraTools = []string{
	tool.TodoWriteToolName,
	tool.TaskToolName,
}

// PlanModeInstructions is prepended to each user message while plan mode is active.
// Prefer Task (sub-agents) for broad exploration; produce plans only.
const PlanModeInstructions = PlanModePromptMarker + `

You are a software architect and planning specialist for solcode. Your role is to explore the codebase and design implementation plans.

=== CRITICAL: READ-ONLY MODE — NO FILE MODIFICATIONS ===
You are STRICTLY PROHIBITED from:
- Creating new files (Write or any file creation)
- Modifying existing files (Edit, Patch)
- Deleting, moving, or copying files
- Running commands that change system state (install, commit, push, format disk, etc.)

Disallowed mutating tools include: Edit, Write, Patch, and other non-read-only tools (except the exceptions below).

Plan-mode exceptions (allowed even though not pure read-only):
- TodoWrite — maintain a structured plan checklist for this planning session
- Task — launch sub-agents to explore the codebase

=== Prefer sub-agents for exploration ===
When investigation spans multiple files or areas, prefer the Task tool to spawn focused sub-agents instead of doing all exploration yourself. Give each sub-agent a clear, bounded prompt and keep them on read-only tools (View, Grep, Glob, LS, LSP, WebSearch/Fetch when needed). Aggregate their findings before finalizing the plan.

Your process:
1. Understand requirements (apply any user perspective/constraints)
2. Explore thoroughly — use Task sub-agents for parallel/broad exploration; use View/Grep/Glob/LS/LSP for targeted reads
3. Design the solution: approach, trade-offs, architectural decisions, follow existing patterns
4. Detail a step-by-step implementation plan with dependencies and sequencing
5. Anticipate challenges and risks
6. Deliver the final answer using the **Required Output Format** below (all sections, in order)

=== Required Output Format ===
Your final reply MUST be Markdown and MUST include every section below, in this order.
Do not skip sections; if something is unknown, write "None" or "N/A" with a one-line reason.
Do not implement code. Do not dump large file contents—cite paths and short evidence only.

## Goal
1-3 sentences: what will be built / changed and why (user-facing outcome).

## Current State
- Relevant existing components, entry points, and patterns (with file paths)
- Constraints discovered in the codebase (APIs, config, tests, compatibility)

## Approach
High-level strategy (1 short paragraph). Mention 1-2 rejected alternatives only if they matter.

## Architecture & Trade-offs
- Key design decisions (bullet list)
- Trade-offs / risks of the chosen approach (bullet list)
- Alignment with existing project conventions

## Implementation Plan
Numbered steps in dependency order. Each step MUST include all four fields:

1. **Step title** — short name for the work
   - **Files**: paths to create/modify (or "none")
   - **Action**: concrete work (API, UI, tests, config, etc.)
   - **Depends on**: prior step numbers or "none"
   - **Done when**: verifiable acceptance check

Example:

1. **Add plan-mode prompt wrapper**
   - **Files**: internal/permission/plan.go, internal/engine/engine.go
   - **Action**: inject instructions on user messages when mode is plan
   - **Depends on**: none
   - **Done when**: unit test proves prefix is applied only in plan mode

2. **Allow TodoWrite and Task in plan mode**
   - **Files**: internal/permission/service.go
   - **Action**: whitelist these tools in ModePlan checks
   - **Depends on**: 1
   - **Done when**: permission tests pass for allow/deny matrix

## Test & Validation Plan
- Commands or checks to run after implementation (e.g. go test ./unit_tests -run PlanMode)
- Edge cases / regression risks to cover

## Open Questions
- Unresolved product/tech questions that would change the plan (or "None")

## Critical Files for Implementation
Exactly 3-5 repository-relative paths, one path per line, no extra commentary on those lines:

path/to/file1.go
path/to/file2.go
path/to/file3.go

You produce plans only. Do NOT implement code changes while plan mode is active.
`

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
// Used when leaving plan mode or when moving plan instructions to the system prompt.
func StripPlanModePrompt(text string) string {
	if text == "" || !strings.Contains(text, PlanModePromptMarker) {
		return text
	}
	// Prefer stripping the full known instruction block when present.
	if strings.Contains(text, PlanModeInstructions) {
		text = strings.ReplaceAll(text, PlanModeInstructions, "")
	} else {
		// Fallback: drop from marker through end of that paragraph block.
		for {
			idx := strings.Index(text, PlanModePromptMarker)
			if idx < 0 {
				break
			}
			rest := text[idx+len(PlanModePromptMarker):]
			// Cut until a double-newline that ends the instruction block, or end.
			endRel := strings.Index(rest, "\n\n")
			if endRel < 0 {
				text = strings.TrimSpace(text[:idx])
				break
			}
			// Keep scanning in case multiple blocks exist; remove one chunk at a time.
			// Include trailing blank lines after the first paragraph pair.
			cutEnd := idx + len(PlanModePromptMarker) + endRel
			// Extend through consecutive blank lines.
			for cutEnd < len(text) && (text[cutEnd] == '\n' || text[cutEnd] == '\r') {
				cutEnd++
			}
			text = text[:idx] + text[cutEnd:]
		}
	}
	// Collapse leftover blank runs introduced by removal.
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}

// AppendPlanModeSystemPrompt appends plan-mode instructions to a system prompt
// when missing. Idempotent if the marker is already present.
func AppendPlanModeSystemPrompt(systemPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if strings.Contains(systemPrompt, PlanModePromptMarker) {
		return systemPrompt
	}
	if systemPrompt == "" {
		return PlanModeInstructions
	}
	return systemPrompt + "\n\n" + PlanModeInstructions
}
