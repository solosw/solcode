package engine

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestContextBlockOmitsRecentSessionStateHeader(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Recent session state:\n- Continue custom provider setup", nil, "")
	if strings.Contains(block, "Recent session state:") {
		t.Fatalf("context block must omit the session summary heading: %q", block)
	}
	if !strings.Contains(block, "Continue custom provider setup") {
		t.Fatalf("context block = %q, want summary content", block)
	}
}

func TestContextBlockOmitsEmptySessionSummary(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Recent session state:", nil, "")
	if block != "" {
		t.Fatalf("context block = %q, want no empty session-summary block", block)
	}
}

func TestContextBlockOmitsNestedEmptySessionSummaryHeaders(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Session summary:\nRecent session state:", nil, "")
	if block != "" {
		t.Fatalf("context block = %q, want no nested empty session-summary block", block)
	}
}

func TestContextBlockOmitsViewPaginationSummaryNoise(t *testing.T) {
	summary := strings.Join([]string{
		"Recent session state:",
		"(File has 1038 more lines. Use 'offset' parameter to read beyond line 260)",
		"(File has 571 more lines. Use 'offset' parameter to read beyond line 425)",
	}, "\n")
	if block := (ContextBuilder{}).contextBlock(summary, nil, ""); block != "" {
		t.Fatalf("context block = %q, want pagination-only summary omitted", block)
	}
}

func TestContextBlockOmitsPlaceholderSessionSummary(t *testing.T) {
	summary := strings.Join([]string{
		"文件变更图上下文",
		"旧 session 对话压缩结果",
		"用户最新 prompt",
		"go test ./internal/app ./internal/engine ./internal/session ./cmd/solcode",
		"go build -o solcode.exe ./cmd/solcode",
	}, "\n")
	if block := (ContextBuilder{}).contextBlock(summary, nil, ""); block != "" {
		t.Fatalf("context block = %q, want placeholder summary omitted", block)
	}
}

func TestWithContextMessagesKeepsProjectSummaryAndLatestPromptOrder(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("latest user prompt")),
	}
	got := (ContextBuilder{}).withContextMessages(
		messages,
		"Recent session state:\n- previous session outcome",
		nil,
		"## Recent tracked changes\n- changed README",
	)
	if len(got) != 2 {
		t.Fatalf("message count = %d, want context plus latest prompt", len(got))
	}
	contextText := got[0].Content[0].OfText.Text
	if !strings.Contains(contextText, "Project knowledge context:") || !strings.Contains(contextText, "Session summary:") {
		t.Fatalf("composed context = %q, want both project knowledge and session summary", contextText)
	}
	if strings.Index(contextText, "Project knowledge context:") > strings.Index(contextText, "Session summary:") {
		t.Fatalf("context order = %q, want project knowledge before session summary", contextText)
	}
	promptText := got[1].Content[0].OfText.Text
	if promptText != "latest user prompt" {
		t.Fatalf("latest prompt = %q, want unchanged latest prompt", promptText)
	}
}

func TestSystemPromptIncludesProjectRules(t *testing.T) {
	builder := ContextBuilder{
		ProjectRules: "Project rules:\nPrefer table-driven tests.",
	}
	got := builder.systemPrompt("/tmp/demo")
	if !strings.Contains(got, "Prefer table-driven tests.") {
		t.Fatalf("system prompt missing project rules: %q", got)
	}
	if !strings.Contains(got, "You are solcode") {
		t.Fatalf("system prompt missing default agent instructions")
	}
	rulesAt := strings.Index(got, "Prefer table-driven tests.")
	wdAt := strings.Index(got, "Working directory: /tmp/demo")
	if rulesAt < 0 || wdAt < 0 || rulesAt > wdAt {
		t.Fatalf("project rules should appear before working directory:\n%s", got)
	}
}

// plan mode 的短规则现在固定在 system prompt 里（不再随模式变化），
// 变的只是动态上下文里的 ACTIVE 指令块。
func TestSystemPromptStableAcrossPlanMode(t *testing.T) {
	base := ContextBuilder{
		SystemPrompt: "Custom preamble.",
		ProjectRules: "Prefer table-driven tests.",
	}
	off := base
	off.PlanMode = false
	off.ModeInstructions = ""

	on := base
	on.PlanMode = true
	on.ModeInstructions = planModeSystemPrompt()

	sysOff := off.systemPrompt("/tmp/demo")
	sysOn := on.systemPrompt("/tmp/demo")
	if sysOff != sysOn {
		t.Fatalf("system prompt must not change when plan mode toggles\noff:\n%s\non:\n%s", sysOff, sysOn)
	}
	// 短规则（READ-ONLY）常驻 system prompt，两种模式都有。
	if !strings.Contains(sysOn, permission.PlanModeShortInstructions) {
		t.Fatalf("system prompt missing compact plan-mode rules: %q", sysOn)
	}
	// 详细的 ACTIVE 指令（角色 + 输出格式）不能进 system prompt。
	if strings.Contains(sysOn, "(ACTIVE)") || strings.Contains(sysOn, "software architect") {
		t.Fatalf("active plan-mode instructions leaked into system prefix: %q", sysOn)
	}
	// 简短规则应只出现一次，不能重复注入。
	if strings.Count(sysOn, "PLAN MODE") != 1 {
		t.Fatalf("compact plan rules must appear exactly once, got %d: %q", strings.Count(sysOn, "PLAN MODE"), sysOn)
	}

	block := on.contextBlock("", nil, "")
	if !strings.Contains(block, "(ACTIVE)") {
		t.Fatalf("plan mode detail must live in dynamic context block, got %q", block)
	}
	blockOff := off.contextBlock("", nil, "")
	if blockOff != "" {
		t.Fatalf("plan mode detail must be absent when ModeInstructions empty: %q", blockOff)
	}
}

func TestSystemPromptOmitsEmptyProjectRules(t *testing.T) {
	got := (ContextBuilder{}).systemPrompt("")
	if strings.Contains(got, "Project rules:") {
		t.Fatalf("empty project rules leaked into system prompt: %q", got)
	}
}

// Batch execution semantics belong in the stable prompt for main and child
// agents, rather than relying on hidden primitive tool descriptions.
func TestSystemPromptDocumentsBatchExecutionContract(t *testing.T) {
	got := (ContextBuilder{}).systemPrompt("/tmp/demo")
	cases := []struct {
		name    string
		phrases []string
	}{
		{"schema and known inputs", []string{"Only child tools defined in the BatchTool schema", "Operation IDs are optional", "Dependencies only order execution", "do not interpolate results", "Read unknown content first"}},
		{"dynamic snapshots", []string{"Dynamic <solcode-context> overlays are snapshots", "Later sections of the same kind supersede earlier ones", "newer tool results take precedence", "Efficiency notes are advisory"}},
		{"targeted evidence recovery", []string{"A file hash or truncated excerpt is not the full file content", "compacted out", "Never invent old_string"}},
		{"atomicity scopes", []string{"MultiEdit defaults to per-file atomicity", "input.atomic=true groups its files", "BatchTool.atomic=true groups all file operations", "separate Edit operations in a non-atomic batch"}},
		{"partial success and rollback", []string{"a failed MultiEdit may still have changed other files", "check atomic_status and reverted", "corrected transaction must be reapplied", "rollback_failed or unknown", "read current targets"}},
		{"error correction and permissions", []string{"On INVALID_INPUT", "on EDIT_MISMATCH", "Do not repeat unchanged invalid calls", "Respect permission/allowlist denials", "allowing only BatchTool does not authorize its children"}},
		{"loss-aware results", []string{"Keep output_mode=compact by default", "Do not routinely retrieve every diff", "full_output_path is present", "targeted View operation with offset/limit", "output_mode=full still obeys output budgets", "output_storage_error"}},
		{"background acknowledgements", []string{"running_in_background", "not successful completion", "wait for its completion notification", "do not poll or relaunch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, phrase := range tc.phrases {
				if !strings.Contains(got, phrase) {
					t.Errorf("system prompt missing %q", phrase)
				}
			}
		})
	}
	for _, obsolete := range []string{"Re-read only when the file was modified since it was cached", "one response covers the whole edit", "multiple operations in ONE BatchTool call"} {
		if strings.Contains(got, obsolete) {
			t.Errorf("over-batching or incomplete-read rule still present: %q", obsolete)
		}
	}
}

func TestPlanModePromptUsesReadOnlyBatchOperations(t *testing.T) {
	for name, prompt := range map[string]string{"stable": permission.PlanModeShortInstructions, "active": permission.PlanModeInstructions} {
		if !strings.Contains(prompt, "BatchTool with only View/Grep/Glob/LS/LSP operations") {
			t.Errorf("%s plan instructions must describe wrapped reads: %s", name, prompt)
		}
	}
	if !strings.Contains(permission.PlanModeInstructions, "do not include edits or Bash") {
		t.Fatal("plan mode prompt must exclude mutation operations")
	}
}

func TestToolDescriptionsAgreeWithBatchRecoveryAndAllowlists(t *testing.T) {
	descriptions := []struct {
		name, text string
		phrases    []string
	}{
		{"BatchTool", tool.NewBatchTool().Description(), []string{"non-atomic batches", "MultiEdit partially fails", "atomic_status and reverted", "after rolled_back", "after rollback_failed/unknown"}},
		{"Task", tool.NewTaskTool(nil).Description(), []string{"list permitted child tool names", "BatchTool alone does not authorize its children"}},
		{"Subagent", tool.NewSubagentTool(nil).Description(), []string{"list permitted child tool names", "BatchTool alone does not authorize its children"}},
	}
	for _, d := range descriptions {
		for _, phrase := range d.phrases {
			if !strings.Contains(d.text, phrase) {
				t.Errorf("%s description missing %q", d.name, phrase)
			}
		}
	}
}
