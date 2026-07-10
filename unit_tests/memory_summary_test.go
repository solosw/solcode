package unit_tests

import (
	"strings"
	"testing"

	app "github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/memory"
)

func TestSummarizeForContextStructuredSections(t *testing.T) {
	transcript := strings.Join([]string{
		`user: 现在记忆太分散了，记忆还应该在后台单独触发才对`,
		`assistant: 我会检查当前 memory trigger 和 retrieval budgeting。`,
		`assistant: [tool use: Edit]`,
		`assistant: {"file_path":"internal/app/app.go","old_string":"a","new_string":"b"}`,
		`user: [tool result]`,
		`user: applied patch successfully`,
		`assistant: [tool use: Bash]`,
		`assistant: {"command":"go test ./internal/app -run TestMemorySummaryTriggerUses70PercentThreshold -v"}`,
		`user: [tool result]`,
		`user: PASS`,
		`assistant: 下一步应该继续把记忆总结改成结构化格式。`,
	}, "\n")

	summary := app.TestOnlySummarizeForContext(transcript, "")
	for _, want := range []string{
		"1. Primary Request and Intent:",
		"2. Key Technical Concepts:",
		"3. Files and Code Sections:",
		"4. Errors and Fixes:",
		"5. Problem Solving:",
		"6. All User Messages:",
		"7. Pending Tasks:",
		"8. Current Work:",
		"9. Optional Next Step:",
		"internal/app/app.go",
		"go test ./internal/app -run TestMemorySummaryTriggerUses70PercentThreshold -v",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected summary to contain %q, got:\n%s", want, summary)
		}
	}
}

func TestSummarizeForContextIncludesPriorSummaryContext(t *testing.T) {
	transcript := "user: 继续\nassistant: 好的，继续完善结构化摘要。"
	previous := "7. Pending Tasks:\n- 完成结构化记忆摘要"
	summary := app.TestOnlySummarizeForContext(transcript, previous)
	if !strings.Contains(summary, "0. Prior Summary Context:") {
		t.Fatalf("expected prior summary section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "完成结构化记忆摘要") {
		t.Fatalf("expected previous summary text retained, got:\n%s", summary)
	}
}

func TestSummarizeForContextMergesExtractedMemoryItems(t *testing.T) {
	items := []memory.Item{
		{Text: "Compacted session file modifications: internal/app/app.go: edited memory trigger and retrieval budget.", Kind: memory.KindTask, Tags: []string{"code-change", "files", "modifications"}},
		{Text: "Compacted session validation/build commands run: go test ./internal/app -run TestMemorySummaryTriggerUses70PercentThreshold -v.", Kind: memory.KindTask, Tags: []string{"validation", "build"}},
		{Text: "Intended retrieval memory budget is 10%-capped with 10k-50k token bounds.", Kind: memory.KindConstraint, Tags: []string{"memory", "budget"}},
	}
	summary := app.TestOnlySummarizeForContextWithItems("user: 继续优化记忆摘要", "", items)
	for _, want := range []string{
		"internal/app/app.go: edited memory trigger and retrieval budget",
		"go test ./internal/app -run TestMemorySummaryTriggerUses70PercentThreshold -v",
		"10%-capped with 10k-50k token bounds",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected merged summary to contain %q, got:\n%s", want, summary)
		}
	}
}

func TestSummarizeForContextSanitizesPollutedPriorSummary(t *testing.T) {
	transcript := strings.Join([]string{
		"user: 继续",
		"assistant: 继续收尾",
		"+ \tvar output strings.Builder",
		"if strings.Contains(lower, \"gofmt\") && strings.Contains(lower, \" -w\") {",
		"[→] Running targeted tests",
		"continue",
		"[ ] Fix compile test regressions summary sanitization changes (pending)",
	}, "\n")
	previous := strings.Join([]string{
		"8. Current Work:",
		"- user: 继续",
		"- Compacted session file modifications: internal/app/app.go: edited (replaced old behavior -> new behavior); internal/engine/engine.go: edited (replaced foo -> bar).",
		"- Compacted session tool usage: Edit, Bash.",
		"- 340| current.ReplaceMessages(session.StripEphemeralContextMessages(current.CopyMessages()))",
		"- ```go",
		`- {"old_string":"x","new_string":"y"}`,
	}, "\n")
	summary := app.TestOnlySummarizeForContext(transcript, previous)
	for _, want := range []string{"0. Prior Summary Context:", "internal/app/app.go: edited", "internal/engine/engine.go: edited", "6. All User Messages:", "user: 继续"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected sanitized prior summary to contain %q, got:\n%s", want, summary)
		}
	}
	for _, unwanted := range []string{"old behavior", "new behavior", "Compacted session tool usage", "340|", "```go", `"old_string"`, `"new_string"`, "+ \tvar output strings.Builder", "if strings.Contains(lower", "[→] Running targeted tests", "[ ] Fix compile test regressions summary sanitization changes (pending)", "- continue"} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("did not expect polluted summary content %q, got:\n%s", unwanted, summary)
		}
	}
}

func TestSummarizeForContextRecompactsPollutedStructuredSummary(t *testing.T) {
	transcript := "user: 继续\nassistant: 继续"
	previous := strings.Join([]string{
		"0. Prior Summary Context:",
		"- user: 继续",
		"- var b strings.Builder",
		"- + \tvar b strings.Builder",
		"- assistant: 我继续直接收尾：先把 `app.go` 的 build 错修掉，再把“加载旧 session 时自动去污 summary”接上，同时更新失效测试。",
		"- for _, want := range []string{\"internal/app/app.go: edited\", \"old behavior\", \"new behavior\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"- + \tfor _, want := range []string{\"internal/app/app.go: edited\", \"targeted replacement\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"- `{" + "\"command\":\"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go\"}" + `,`,
		"",
		"1. Primary Request and Intent:",
		"- Compacted session file modifications: internal/anthropic/messages.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement).",
		"",
		"2. Key Technical Concepts:",
		"- Compacted session validation/build commands run: \"); idx >= 0 {.",
		"- + \t\t\treturn \"Compacted session validation/build commands run.\"",
		"- 我先 `gofmt`，再用 **逐条精确测试名** 的方式重跑，避免 shell 引号问题。",
		"",
		"3. Files and Code Sections:",
		"- internal/memory/sanitize.go",
		"- files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"",
		"4. Errors and Fixes:",
		"- item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"- +\t\tline = strings.TrimPrefix(line, \"- \")",
		"",
		"5. Problem Solving:",
		"- assistant: 我继续做最后这层“历史 memory 读时自愈”，先把相关合并/存储点定位出来，然后直接补 retrieval 回写清洗。",
		"",
		"6. All User Messages:",
		"- user: 继续",
		"",
		"7. Pending Tasks:",
		"- [ ] Add regression tests stored memory self-healing retrieval (pending)",
		"- [→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"",
		"8. Current Work:",
		"- +\t\treturn !isTrivialContinuationSummaryLine(line)",
		"- @@ -810,7 +810,9 @@",
		"- currentWork = []string{primary}",
		"",
		"9. Optional Next Step:",
		"- user: 继续",
		"- assistant: 我继续做最后这层“历史 memory 读时自愈”，先把相关合并/存储点定位出来，然后直接补 retrieval 回写清洗。",
	}, "\n")

	summary := app.TestOnlySummarizeForContext(transcript, previous)
	for _, want := range []string{
		"0. Prior Summary Context:",
		"internal/anthropic/messages.go: edited (targeted replacement)",
		"internal/app/app.go: edited (targeted replacement)",
		"3. Files and Code Sections:",
		"internal/memory/sanitize.go",
		"6. All User Messages:",
		"user: 继续",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected recompacted summary to contain %q, got:\n%s", want, summary)
		}
	}
	for _, unwanted := range []string{
		"var b strings.Builder",
		"assistant: 我继续",
		"for _, want :=",
		`"command":"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go"`,
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"return \"Compacted session validation/build commands run.\"",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
	} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("did not expect polluted structured summary content %q, got:\n%s", unwanted, summary)
		}
	}
	if strings.Contains(summary, "internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement)") {
		t.Fatalf("expected duplicated file modification entries inside compacted line to collapse, got:\n%s", summary)
	}
}

func TestSummarizeForContextRecompactsExactPollutedSessionSummarySample(t *testing.T) {
	transcript := "user: 继续\nassistant: 继续"
	previous := strings.Join([]string{
		"0. Prior Summary Context:",
		"- user: 继续",
		"- var b strings.Builder",
		"- + \tvar b strings.Builder",
		"- assistant: 我继续直接收尾：先把 `app.go` 的 build 错修掉，再把“加载旧 session 时自动去污 summary”接上，同时更新失效测试。",
		"- assistant: 我继续直接修，先把 **build/test 断点** 和 **旧 session summary 去污入口** 一起收掉。",
		"- for _, want := range []string{\"internal/app/app.go: edited\", \"old behavior\", \"new behavior\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"- + \tfor _, want := range []string{\"internal/app/app.go: edited\", \"targeted replacement\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"- `{" + "\"command\":\"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go\"}" + `,`,
		"",
		"1. Primary Request and Intent:",
		"- Compacted session file modifications: internal/anthropic/messages.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement).",
		"",
		"2. Key Technical Concepts:",
		"- Compacted session file modifications: internal/anthropic/messages.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement).",
		"- Compacted session validation/build commands run: \"); idx >= 0 {.",
		"- Compacted session validation/build commands run: \"):]).",
		"- +\t\t\treturn \"Compacted session validation/build commands run.\"",
		"- Compacted session validation/build commands run: \" + strings.Join(cleaned, \"; \") + \".\".",
		"- +\t\t\"+ \tvar output strings.Builder\",",
		"- +\t\t\"if strings.Contains(lower, \\\"gofmt\\\") && strings.Contains(lower, \\\" -w\\\") {\",",
		"- 我先 `gofmt`，再用 **逐条精确测试名** 的方式重跑，避免 shell 引号问题。",
		"- \"+ \tvar output strings.Builder\",",
		"- \"if strings.Contains(lower, \\\"gofmt\\\") && strings.Contains(lower, \\\" -w\\\") {\",",
		"",
		"3. Files and Code Sections:",
		"- Compacted session file modifications: internal/anthropic/messages.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement).",
		"- internal/memory/sanitize.go",
		"- internal/memory/memory.go",
		"- internal/memory/manager.go",
		"- internal/memory/sanitize_test.go",
		"- internal/app/app.go",
		"- unit_tests/memory_summary_test.go",
		"- files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"",
		"4. Errors and Fixes:",
		"- item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"- func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"- +\t\t\tpart = storedExcerpt(strings.TrimSpace(strings.TrimSuffix(part, \".\")), 120)",
		"- +\t\tline = strings.TrimPrefix(line, \"- \")",
		"- +\t\tline = strings.TrimPrefix(line, \"+ \")",
		"- +\tpart = strings.TrimSpace(strings.TrimSuffix(part, \".\"))",
		"- +\tif storedLineNumberPattern.MatchString(lower) || strings.HasPrefix(line, \"```\") {",
		"- line = strings.TrimPrefix(line, \"- \")",
		"",
		"5. Problem Solving:",
		"- assistant: 我继续做最后这层“历史 memory 读时自愈”，先把相关合并/存储点定位出来，然后直接补 retrieval 回写清洗。",
		"- assistant: `Write` 被拒了，所以我改用更小粒度的文件创建方式继续，不重复同一个调用。先把缺失的 `sanitize.go` 补上，再加回归测试。",
		"- assistant: 我继续把这层做完整：",
		"- assistant: 我继续先验这层“历史 memory 自愈”是否真的生效，再看要不要补 `Manager.Retrieve` 级别的回归。",
		"- assistant: 这份 `Session summary` 说明还有一层摘要级噪声没截干净：",
		"- assistant: 剩下的问题已经很明确：`Prior Summary Context` 还会吸进 **diff 行、代码行、todo 状态行、assistant/user 角色行**。",
		"- assistant: 这份贴出来的 summary 已经把漏网噪声定位清楚了：",
		"- assistant: 你贴出来的 summary 已经把漏网模式完整暴露了：",
		"",
		"6. All User Messages:",
		"- user: 继续",
		"",
		"7. Pending Tasks:",
		"- [ ] Add regression tests stored memory self-healing retrieval (pending)",
		"- [ ] Run targeted tests memory sanitization summary generation (pending)",
		"- +\t\t\tcontinue",
		"- +\t\tnext, _, keep := sanitizeStoredMemoryItem(item)",
		"- +\t\tcleaned = append(cleaned, next)",
		"- **旧 session summary 里的 diff 行 / 代码行 / todo 状态行** 还会进入 `Prior Summary Context`。",
		"- [→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"- [ ] Update regression tests polluted prior summary stored memory self-healing (pending)",
		"",
		"8. Current Work:",
		"- Compacted session file modifications: internal/anthropic/messages.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement).",
		"- +\t\treturn !isTrivialContinuationSummaryLine(line)",
		"- +\tprimary := firstSummaryLine(primaryCandidates, firstSummaryLine(recentWork, previous))",
		"- files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"- problems := summarizeProblemLines(append(lines, extractRelevantPriorHints(priorHints, []string{\"errors\", \"fixes\"})...))",
		"- @@ -810,7 +810,9 @@",
		"- pending := summarizePending(append(lines, priorHints...), compactPreviousSummary(previous))",
		"- -\tcurrentWork := limitSummaryLines(append(append(priorityCurrent, recentWork...), extractRelevantPriorHints(priorHints, []string{\"current work\"})...), 12)",
		"- +\tcurrentWork := limitSummaryLines(filterSummaryLines(append(append(priorityCurrent, recentWork...), extractRelevantPriorHints(priorHints, []string{\"current work\"})...), func(line string) bool {",
		"- +\t}), 12)",
		"- if len(currentWork) == 0 {",
		"- currentWork = []string{primary}",
		"",
		"9. Optional Next Step:",
		"- user: 继续",
		"- assistant: 我继续做最后这层“历史 memory 读时自愈”，先把相关合并/存储点定位出来，然后直接补 retrieval 回写清洗。",
		"- +\t\t\tcontinue",
		"- +\t\tnext, _, keep := sanitizeStoredMemoryItem(item)",
		"- +\t\tcleaned = append(cleaned, next)",
		"- assistant: `Write` 被拒了，所以我改用更小粒度的文件创建方式继续，不重复同一个调用。先把缺失的 `sanitize.go` 补上，再加回归测试。",
	}, "\n")

	summary := app.TestOnlySummarizeForContext(transcript, previous)
	for _, want := range []string{
		"0. Prior Summary Context:",
		"internal/anthropic/messages.go: edited (targeted replacement)",
		"internal/app/app.go: edited (targeted replacement)",
		"internal/engine/engine.go: edited (targeted replacement)",
		"3. Files and Code Sections:",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
		"6. All User Messages:",
		"user: 继续",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected exact polluted sample to sanitize into %q, got:\n%s", want, summary)
		}
	}
	for _, unwanted := range []string{
		"var b strings.Builder",
		"assistant: 我继续",
		"assistant: `Write` 被拒了",
		"for _, want :=",
		`"command":"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go"`,
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"Compacted session validation/build commands run: \"):]).",
		"Compacted session validation/build commands run: \" + strings.Join(cleaned, \"; \") + \".\".",
		"return \"Compacted session validation/build commands run.\"",
		"+ \tvar output strings.Builder",
		"if strings.Contains(lower, \"gofmt\")",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[ ] Run targeted tests memory sanitization summary generation (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
		"problems := summarizeProblemLines(append(lines",
		"pending := summarizePending(append(lines, priorHints())",
	} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("did not expect exact polluted sample content %q, got:\n%s", unwanted, summary)
		}
	}
	if strings.Contains(summary, "internal/app/app.go: edited (targeted replacement); internal/app/app.go: edited (targeted replacement)") {
		t.Fatalf("expected duplicated app.go modifications inside compacted line to collapse, got:\n%s", summary)
	}
	if strings.Contains(summary, "internal/engine/engine.go: edited (targeted replacement); internal/engine/engine.go: edited (targeted replacement)") {
		t.Fatalf("expected duplicated engine.go modifications inside compacted line to collapse, got:\n%s", summary)
	}
}
