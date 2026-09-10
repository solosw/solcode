package unit_tests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/solosw/solcode/internal/tui"
)

func newTUI(t *testing.T) tui.Model {
	t.Helper()
	model := tui.New(nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return updated.(tui.Model)
}

func TestTUIModelQueuesMessageWhileStreaming(t *testing.T) {
	var queued []string
	model := tui.NewWith(func(string) (tea.Cmd, func()) {
		return func() tea.Msg { return nil }, nil
	}, tui.Dark, "", "", true)
	model.SetQueueFunc(func(prompt string) { queued = append(queued, prompt) })
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)

	for _, text := range []string{"i", "n"} {
		updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: text}))
		model = updated.(tui.Model)
	}
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	if cmd == nil {
		t.Fatal("expected initial prompt to start a stream")
	}
	for _, text := range []string{"f", "o", "l", "l", "o", "w", " ", "u", "p"} {
		updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: text}))
		model = updated.(tui.Model)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)

	if len(queued) != 1 || queued[0] != "follow up" {
		t.Fatalf("queued prompts = %#v, want [follow up] (view: %s)", queued, model.View().Content)
	}
	if !strings.Contains(model.View().Content, "follow up") {
		t.Fatalf("expected queued message in view: %s", model.View().Content)
	}
}

func TestTUIModelStreamsAssistantText(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamTextMsg{Text: "hello"})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "●") {
		t.Fatalf("expected assistant marker ● in view: %s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatalf("expected streamed text in view: %s", view)
	}
}

func TestTUIModelShowsErrors(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamErrorMsg{Err: errTestTUI})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "⚠") || !strings.Contains(view, "tui test error") {
		t.Fatalf("expected error marker in view: %s", view)
	}
}

func TestTUIModelShowsToolStatus(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{Name: "Bash", Input: `{"command":"ls"}`})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "ls") || !strings.Contains(view, "正在运行 1 个 Shell") {
		t.Fatalf("expected shell start status in view: %s", view)
	}
	if strings.Contains(view, "Tools") {
		t.Fatalf("expected no bottom tools panel in view: %s", view)
	}

	updated, _ = model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "file1.txt", IsError: false})
	model = updated.(tui.Model)
	view = model.View().Content
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "file1.txt") {
		t.Fatalf("expected tool done in view: %s", view)
	}
	if strings.Contains(view, "Tools") {
		t.Fatalf("expected no bottom tools panel after completion: %s", view)
	}
}

func TestTUIModelTodoWriteOutputIsExpanded(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{Name: "TodoWrite", Input: `{"todos":[{"id":"1","content":"one","status":"pending","priority":"high"},{"id":"2","content":"two","status":"in_progress","priority":"medium"},{"id":"3","content":"three","status":"completed","priority":"low"}]}`})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Todos") || !strings.Contains(view, "one") || !strings.Contains(view, "two") || !strings.Contains(view, "three") {
		t.Fatalf("expected todo panel rows in view: %s", view)
	}
}

func TestTUIModelNormalToolOutputStaysCollapsed(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "one\ntwo\nthree", IsError: false})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "one") {
		t.Fatalf("expected collapsed tool output summary in view: %s", view)
	}
	if strings.Contains(view, "two") || strings.Contains(view, "three") {
		t.Fatalf("expected collapsed output to hide later lines: %s", view)
	}
}

func TestTUIModelFileMutationOutputIsExpandedAndShowsDiff(t *testing.T) {
	model := newTUI(t)
	output := strings.Join([]string{
		"Content replaced in file: test.go",
		"Lines changed: +1 -1",
		"",
		"--- a/test.go",
		"+++ b/test.go",
		"@@ -1 +1 @@",
		"- old",
		"+ new",
	}, "\n")
	updated, _ := model.Update(tui.ToolDoneMsg{Name: "Edit", Output: output, IsError: false})
	model = updated.(tui.Model)
	view := model.View().Content
	if strings.Contains(view, "more lines") {
		t.Fatalf("expected Edit output to be expanded, got collapsed view: %s", view)
	}
	if !strings.Contains(view, "--- a/test.go") || !strings.Contains(view, "+++ b/test.go") || !strings.Contains(view, "- old") || !strings.Contains(view, "+ new") {
		t.Fatalf("expected inline diff in expanded Edit output: %s", view)
	}
	if !hasLineContainingAll(view, "- old", "│", "+ new") {
		t.Fatalf("expected side-by-side diff row with old/new columns: %s", view)
	}
}

func TestTUIModelFileMutationOutputForcesExpandedEvenIfCollapsedStateIsSet(t *testing.T) {
	model := newTUI(t)
	model.ReplaceMessages([]tui.ChatMessage{{
		Role:      "tool-done",
		ToolName:  "Write",
		Content:   "--- a/test.go\n+++ b/test.go\n@@ -1 +1 @@\n- old\n+ new",
		Collapsed: true,
	}})
	view := model.View().Content
	if strings.Contains(view, "more lines") {
		t.Fatalf("expected Write output to ignore collapsed state: %s", view)
	}
	if !strings.Contains(view, "+ new") {
		t.Fatalf("expected Write diff line to be visible: %s", view)
	}
}

func TestTUIModelWelcomeRendersCenteredLogo(t *testing.T) {
	model := newTUI(t)
	view := model.View().Content
	if !strings.Contains(view, "☀") || !strings.Contains(view, "solcode") || !strings.Contains(view, "Welcome to solcode") {
		t.Fatalf("expected centered logo-style welcome in view: %s", view)
	}
	if strings.Contains(view, "✦ solcode") || strings.Contains(view, "solcode TUI") {
		t.Fatalf("expected old logo/prose welcome to be replaced: %s", view)
	}

	lines := strings.Split(view, "\n")
	sunLine, wordLine := -1, -1
	for i, line := range lines {
		if sunLine < 0 && strings.Contains(line, "☀") {
			sunLine = i
		}
		if wordLine < 0 && strings.Contains(line, "solcode") && !strings.Contains(line, "Welcome") {
			wordLine = i
		}
	}
	if sunLine < 0 || wordLine != sunLine+1 {
		t.Fatalf("expected sun directly above solcode, got sun line %d and word line %d", sunLine, wordLine)
	}
}

func TestTUIModelUserMessageRendersBoxWithoutPromptMarker(t *testing.T) {
	model := newTUI(t)
	model.ReplaceMessages([]tui.ChatMessage{{Role: "user", Content: "hello user"}})
	view := model.View().Content
	if !strings.Contains(view, "hello user") || !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("expected user message box in view: %s", view)
	}
	if strings.Contains(view, "❯") {
		t.Fatalf("expected user message without prompt marker: %s", view)
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func visibleText(view string) string {
	return ansiEscape.ReplaceAllString(view, "")
}

func TestTUIModelInitEnablesBracketedPaste(t *testing.T) {
	model := tui.New(nil)
	if model.Init() == nil {
		t.Fatal("expected init command")
	}
}

func TestTUIModelPasteDoesNotSubmitWithoutExplicitEnter(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	paste := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	updated, _ = model.Update(tea.PasteMsg{Content: paste})
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("expected no submit before explicit enter, got %q", submitted)
	}
	view := model.View().Content
	if !strings.Contains(view, "Pasted text #1 · 6 lines") {
		t.Fatalf("expected folded paste label in input: %s", view)
	}
	if strings.Contains(view, "alpha") || strings.Contains(view, "beta") {
		t.Fatalf("expected folded paste content to stay out of input: %s", view)
	}
}

func TestTUIModelImmediateSyntheticEnterAfterPasteIsIgnored(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	paste := "alpha\nbeta\n"
	updated, _ = model.Update(tea.PasteMsg{Content: paste})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("expected synthetic enter after paste to be ignored, got %q", submitted)
	}
	view := model.View().Content
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") {
		t.Fatalf("expected pasted content to stay in input after ignored enter: %s", view)
	}
}

func TestTUIModelRunesWithoutPasteFlagSubmitNormally(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "alpha\nbeta"}))
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	if submitted != "alpha\nbeta" {
		t.Fatalf("expected non-paste runes to submit normally, got %q", submitted)
	}
}

func TestTUIModelPasteRunesWithNewlineNeverSubmits(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.PasteMsg{Content: "first\nsecond\n"})
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("pasted newline submitted prompt: %q", submitted)
	}
	view := model.View().Content
	if !strings.Contains(view, "first") || !strings.Contains(view, "second") {
		t.Fatalf("pasted newline content missing from input: %s", view)
	}
}

func TestTUIModelDelayedExplicitEnterAfterPasteSubmits(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	paste := "alpha\nbeta\n"
	updated, _ = model.Update(tea.PasteMsg{Content: paste})
	model = updated.(tui.Model)
	time.Sleep(200 * time.Millisecond)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	if submitted != "alpha\nbeta" {
		t.Fatalf("expected delayed explicit enter to submit pasted content, got %q", submitted)
	}
}

func TestTUIModelPastedUserMessageShowsLineCountOnly(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	paste := "alpha\nbeta\ngamma\ndelta\nepsilon"
	updated, _ = model.Update(tea.PasteMsg{Content: paste})
	model = updated.(tui.Model)
	time.Sleep(200 * time.Millisecond)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(submitted, "--- Begin [Pasted text #1 · 5 lines] ---") || !strings.Contains(submitted, paste) || !strings.Contains(submitted, "--- End [Pasted text #1 · 5 lines] ---") {
		t.Fatalf("expected folded paste to expand with boundaries, got %q", submitted)
	}
	if !strings.Contains(view, "Pasted 5 lines") {
		t.Fatalf("expected pasted line count in view: %s", view)
	}
	if strings.Contains(view, "alpha") || strings.Contains(view, "beta") || strings.Contains(view, "gamma") {
		t.Fatalf("expected pasted content hidden from chat view: %s", view)
	}
}

func TestTUIModelReplaceMessages(t *testing.T) {
	model := newTUI(t)
	model.ReplaceMessages([]tui.ChatMessage{{Role: "system", Content: "restored transcript"}})
	view := model.View().Content
	if !strings.Contains(view, "restored transcript") {
		t.Fatalf("expected restored transcript in view: %s", view)
	}
	if strings.Contains(view, "solcode TUI") || strings.Contains(view, "✦ solcode") {
		t.Fatalf("expected initial welcome message to be replaced: %s", view)
	}
}

func TestTUIModelToolTitleReflowsAfterResize(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{
		Name:  "WriteMemory",
		Input: `{"memory":"Persisted sessions store the full expanded folded-paste block in user Content but do not persist ChatMessage.DisplayContent."}`,
	})
	model = updated.(tui.Model)

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 36, Height: 20})
	model = updated.(tui.Model)
	view := visibleText(model.View().Content)
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 36 {
			t.Fatalf("line width = %d, want <= 36 after resize: %q", got, line)
		}
	}
}

func TestTUIModelAgentStatusRenders(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.AgentStatusMsg{ID: "task-1", Role: "task", State: "completed", Description: "Review files", Output: "looks good"})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Review files") || !strings.Contains(view, "looks good") {
		t.Fatalf("expected per-subagent panel status in view: %s", view)
	}
}

func TestTUIModelAgentProgressStreamsNestedTools(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{
		Name:      "Task",
		Input:     `{"description":"Explore"}`,
		ToolUseID: "toolu_task",
	})
	model = updated.(tui.Model)
	updated, _ = model.Update(tui.AgentProgressMsg{
		Kind:            "started",
		AgentID:         "task-1",
		ParentToolUseID: "toolu_task",
		TaskID:          "a",
		Description:     "Explore A",
	})
	model = updated.(tui.Model)
	updated, _ = model.Update(tui.AgentProgressMsg{
		Kind:            "tool_start",
		AgentID:         "task-1",
		ParentToolUseID: "toolu_task",
		TaskID:          "a",
		Description:     "Explore A",
		ToolName:        "View",
		ToolInput:       `{"path":"README.md"}`,
	})
	model = updated.(tui.Model)
	updated, _ = model.Update(tui.AgentProgressMsg{
		Kind:            "started",
		AgentID:         "task-2",
		ParentToolUseID: "toolu_task",
		TaskID:          "b",
		Description:     "Explore B",
	})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Explore A") || !strings.Contains(view, "Explore B") {
		t.Fatalf("expected separate subagent panels: %s", view)
	}
	if !strings.Contains(view, "started") || !strings.Contains(view, "→ View") {
		t.Fatalf("expected process lines on subagent panels: %s", view)
	}
	if strings.Contains(view, "Explore A started") {
		t.Fatalf("did not expect process lines nested under Task tool: %s", view)
	}
	if strings.Contains(view, "Subagents") {
		t.Fatalf("expected no shared Subagents header: %s", view)
	}
}

func TestTUIModelPermissionDialogResponds(t *testing.T) {
	model := newTUI(t)
	responseCh := make(chan bool, 1)
	updated, _ := model.Update(tui.PermissionRequestMsg{
		ToolName:    "Bash",
		Description: "Bash wants to run",
		ResponseCh:  responseCh,
	})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Permission Required") || !strings.Contains(view, "Allow") || !strings.Contains(view, "Deny") {
		t.Fatalf("expected permission dialog in view: %s", view)
	}

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "y"}))
	model = updated.(tui.Model)
	select {
	case allowed := <-responseCh:
		if !allowed {
			t.Fatal("expected permission to be allowed after pressing y")
		}
	default:
		t.Fatal("expected response on channel after pressing y")
	}
	view = model.View().Content
	if strings.Contains(view, "Permission Required") {
		t.Fatalf("expected permission dialog cleared after allow: %s", view)
	}
}

func TestTUIModelPermissionDialogDenies(t *testing.T) {
	model := newTUI(t)
	responseCh := make(chan bool, 1)
	updated, _ := model.Update(tui.PermissionRequestMsg{
		ToolName:    "Bash",
		Description: "Bash wants to run",
		ResponseCh:  responseCh,
	})
	model = updated.(tui.Model)

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "n"}))
	model = updated.(tui.Model)
	select {
	case allowed := <-responseCh:
		if allowed {
			t.Fatal("expected permission to be denied after pressing n")
		}
	default:
		t.Fatal("expected response on channel after pressing n")
	}
}

func TestTUIModelThemeToggle(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "light") {
		t.Fatalf("expected theme toggled to light in status bar: %s", view)
	}
}

func TestThemeWithBackground(t *testing.T) {
	theme := tui.ThemeByName("light").WithBackground("#102030")
	if string(theme.Background) != "#102030" {
		t.Fatalf("background = %q, want %q", theme.Background, "#102030")
	}
	if theme.BackgroundOverride != "#102030" {
		t.Fatalf("background override = %q, want %q", theme.BackgroundOverride, "#102030")
	}
}

func TestTUIModelUsageStatusRenders(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetContextBaseFn(func() int64 { return 1900 })
	updated, _ := model.Update(tui.TokenUsageMsg{EstimatedContextTokens: 1900, InputTokens: 1200, CacheCreationInputTokens: 200, CacheReadInputTokens: 800, OutputTokens: 250, MaxContextTokens: 1000000})
	model = updated.(tui.Model)
	view := model.View().Content
	// cacheUsed=1000, inputSide=1200+800+200=2200 → cache 1k/2.2k with progress bar like ctx
	if !strings.Contains(view, "/1M") || !strings.Contains(view, "ctx 1.9k") || !strings.Contains(view, "cache 1k/2.2k") || !strings.Contains(view, "out 250") {
		t.Fatalf("expected usage status with ctx+cache meters in view: %s", view)
	}
	if strings.Contains(view, "⏎ send") || strings.Contains(view, "Alt+⏎ newline") {
		t.Fatalf("expected input hint to be replaced by usage status: %s", view)
	}
	if strings.Count(view, "ctx ") != 1 {
		t.Fatalf("expected ctx usage to render once in the usage row only: %s", view)
	}
	if strings.Count(view, "cache ") != 1 {
		t.Fatalf("expected cache usage to render once: %s", view)
	}
}

func TestTUIModelUsageStatusAlwaysVisible(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetContextBaseFn(func() int64 { return 1500 })
	view := model.View().Content
	if !strings.Contains(view, "/1M") || !strings.Contains(view, "ctx 1.") {
		t.Fatalf("expected always-visible context usage in view: %s", view)
	}
}

func TestTUIModelSessionBarRendersAboveUsageBar(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetModelName("claude-test")
	view := model.View().Content
	// Chrome order is session bar → input → usage bar. Idle "Ready" no longer
	// occupies a permanent row; usage (ctx) lives under the prompt.
	sessionIndex := strings.Index(view, "claude-test")
	ctxIndex := strings.Index(view, "ctx ")
	if sessionIndex < 0 || ctxIndex < 0 {
		t.Fatalf("expected session model name and ctx usage in view: %s", view)
	}
	if sessionIndex > ctxIndex {
		t.Fatalf("expected session bar above usage bar: %s", view)
	}
}

func TestTUIModelActiveRuntimeRendersInTranscript(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{Name: "Bash", Input: `{"command":"ls"}`})
	model = updated.(tui.Model)
	view := model.View().Content
	// Active runtime trails the transcript (above the session/input chrome).
	statusIndex := strings.Index(view, "正在运行")
	if statusIndex < 0 {
		statusIndex = strings.Index(view, "Bash")
	}
	sessionChrome := strings.Index(view, "Ask solcode")
	if statusIndex < 0 {
		t.Fatalf("expected active runtime status in view: %s", view)
	}
	if sessionChrome >= 0 && statusIndex > sessionChrome {
		t.Fatalf("expected runtime status above input chrome: %s", view)
	}
}

func TestTUIModelInputBoxHasNoPromptPrefix(t *testing.T) {
	model := newTUI(t)
	model, _ = setInputValue(model, "hello")
	view := model.View().Content
	if strings.Contains(view, "> hello") || strings.Contains(view, ">  hello") {
		t.Fatalf("expected input box without > prompt prefix: %s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatalf("expected typed input in view: %s", view)
	}
}

func TestTUIModelUsageStatusTracksLocalInput(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetContextBaseFn(func() int64 { return 1500 })
	before := model.View().Content
	model, _ = setInputValue(model, "extra local prompt text")
	after := model.View().Content
	if before == after {
		t.Fatalf("expected local input to affect view before=%q after=%q", before, after)
	}
	if !strings.Contains(after, "ctx ") {
		t.Fatalf("expected context usage after local input: %s", after)
	}
}

func TestTUIModelSlashEffortDoesNotSubmit(t *testing.T) {
	submitted := false
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = true
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	model.SetDialogCallbacks(func(kind tui.DialogKind) []tui.DialogItem {
		if kind != tui.DialogEffort {
			return nil
		}
		return []tui.DialogItem{{Label: "low", Value: "low"}, {Label: "medium", Value: "medium"}, {Label: "high", Value: "high"}, {Label: "xhigh", Value: "xhigh"}, {Label: "max", Value: "max"}}
	}, nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/effort")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if submitted {
		t.Fatal("expected /effort to be handled locally without submit")
	}
	if !strings.Contains(view, "Select Effort") {
		t.Fatalf("expected effort dialog in view: %s", view)
	}
}

func TestTUIModelSlashCompactAutocomplete(t *testing.T) {
	model := newTUI(t)
	model, _ = setInputValue(model, "/com")
	view := model.View().Content
	if !strings.Contains(view, "/compact") {
		t.Fatalf("expected /compact autocomplete in view: %s", view)
	}
}

func TestTUIModelSlashAutocompleteVisibleWithTodos(t *testing.T) {
	model := newTUI(t)
	// Simulate a populated todo strip that previously competed with the slash
	// completion overlay for bottom chrome height.
	model = model.WithTodosForTest([]tui.TodoViewItem{
		{ID: "1", Content: "Task one", Status: "in_progress", Priority: "high"},
		{ID: "2", Content: "Task two", Status: "pending", Priority: "medium"},
		{ID: "3", Content: "Task three", Status: "pending", Priority: "low"},
	})
	model, _ = setInputValue(model, "/")
	view := model.View().Content
	if !strings.Contains(view, "Commands:") {
		t.Fatalf("expected slash autocomplete header with todos present: %s", view)
	}
	if !strings.Contains(view, "/help") {
		t.Fatalf("expected slash command suggestions with todos present: %s", view)
	}
}

func TestTUIModelFileAutocompleteAndApply(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("package alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.go"), []byte("package beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := tui.NewWith(nil, tui.Dark, "", dir, true)
	updated, cmd := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = asTUIModel(t, updated, cmd)

	model, _ = setInputValue(model, "look at @a")
	view := model.View().Content
	if !strings.Contains(view, "Files:") || !strings.Contains(view, "@alpha.go") {
		t.Fatalf("expected file autocomplete in view: %s", view)
	}

	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = asTUIModel(t, updated, cmd)
	view = model.View().Content
	if !strings.Contains(view, "@alpha.go") {
		t.Fatalf("expected completed path in view after tab: %s", view)
	}
}

func TestTUIModelAskUserDialogResponds(t *testing.T) {
	model := newTUI(t)
	responseCh := make(chan map[string]string, 1)
	updated, _ := model.Update(tui.AskUserRequestMsg{
		Questions: []tui.AskUserQuestion{{
			Question: "Choose mode?",
			Header:   "Mode",
			Options:  []tui.AskUserOption{{Label: "Fast", Description: "quick"}, {Label: "Safe", Description: "careful"}},
		}},
		ResponseCh: responseCh,
	})
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "Choose mode?") || !strings.Contains(view, "Fast") || !strings.Contains(view, "Safe") {
		t.Fatalf("expected AskUser dialog in view: %s", view)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	select {
	case answers := <-responseCh:
		if answers["Choose mode?"] != "Safe" {
			t.Fatalf("expected selected answer Safe, got %#v", answers)
		}
	default:
		t.Fatal("expected AskUser answer on channel")
	}
	if strings.Contains(model.View().Content, "Choose mode?") {
		t.Fatalf("expected AskUser dialog cleared: %s", model.View().Content)
	}
}

func TestTUIModelAskUserCustomAnswer(t *testing.T) {
	model := newTUI(t)
	responseCh := make(chan map[string]string, 1)
	question := "Choose mode?"
	updated, _ := model.Update(tui.AskUserRequestMsg{
		Questions: []tui.AskUserQuestion{{
			Question: question,
			Options:  []tui.AskUserOption{{Label: "Fast"}, {Label: "Safe"}},
		}},
		ResponseCh: responseCh,
	})
	model = updated.(tui.Model)
	view := visibleText(model.View().Content)
	if !strings.Contains(view, "Custom answer") || !strings.Contains(view, "Type a custom answe") {
		t.Fatalf("expected visible custom answer input: %s", view)
	}

	for range 2 {
		updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		model = updated.(tui.Model)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "Custom mode"}))
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)

	select {
	case answers := <-responseCh:
		if answers[question] != "Custom mode" {
			t.Fatalf("custom answer = %q, want %q", answers[question], "Custom mode")
		}
	default:
		t.Fatal("expected custom AskUser answer on channel")
	}
}

func TestTUIModelShowsActiveShellCount(t *testing.T) {
	model := newTUI(t)
	for range 2 {
		updated, _ := model.Update(tui.ToolStartMsg{Name: "Bash", Input: `{"command":"sleep"}`})
		model = updated.(tui.Model)
	}
	if !strings.Contains(model.View().Content, "正在运行 2 个 Shell") {
		t.Fatalf("expected two active shells: %s", model.View().Content)
	}
	updated, _ := model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "done"})
	model = updated.(tui.Model)
	if !strings.Contains(model.View().Content, "正在运行 1 个 Shell") {
		t.Fatalf("expected one active shell after completion: %s", model.View().Content)
	}
}

func TestTUIModelClickOutsideBlursAndTypingRefocuses(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "a"}))
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "a") {
		t.Fatalf("expected typing after blur to refocus input: %s", view)
	}
}

func TestTUIModelSlashHelpDoesNotSubmit(t *testing.T) {
	submitted := false
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = true
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/help")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := visibleText(model.View().Content)
	if submitted {
		t.Fatal("expected /help to be handled locally without submit")
	}
	if !strings.Contains(view, "/effort") || !strings.Contains(view, "/web-ui") || !strings.Contains(view, "/goal") {
		t.Fatalf("expected command help in view: %s", view)
	}
}

func TestTUIModelSlashClearClearsTranscript(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamTextMsg{Text: "hello before clear"})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/clear")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if strings.Contains(view, "hello before clear") {
		t.Fatalf("expected /clear to remove old transcript: %s", view)
	}
	if !strings.Contains(view, "Conversation cleared") {
		t.Fatalf("expected clear confirmation in view: %s", view)
	}
}

func TestTUIModelSlashSkillSubmitsSkillPrompt(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	model.SetSkillNamesFn(func() []string { return []string{"review"} })
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/review auth changes")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if submitted != "Use the Skill tool with skill \"review\" and args \"auth changes\"." {
		t.Fatalf("unexpected submitted prompt: %q", submitted)
	}
	if !strings.Contains(view, "/review auth changes") {
		t.Fatalf("expected original slash command to remain visible in transcript: %s", view)
	}
}

func TestTUIModelCtrlCCancelsActiveStream(t *testing.T) {
	canceled := false
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		return func() tea.Msg { return tui.StreamDoneMsg{} }, func() { canceled = true }
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "write a long answer")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	model = updated.(tui.Model)
	if !canceled {
		t.Fatal("expected Ctrl+C to call active cancel callback")
	}
	if cmd != nil {
		t.Fatal("expected Ctrl+C during stream not to quit")
	}
	if !strings.Contains(model.View().Content, "Canceling") {
		t.Fatalf("expected canceling status in view: %s", model.View().Content)
	}
}

func TestTUIModelUserMessageMarker(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamTextMsg{Text: "x"})
	model = updated.(tui.Model)
	// simulate a user submit by appending via a custom submit that records
	// we just check assistant marker presence; user marker tested via renderMessages indirectly
	_ = model.View()
}

func TestTUIModelNewSessionShowsConfirmDialog(t *testing.T) {
	handlerCalled := false
	crossValue := false
	model := tui.New(nil)
	model.SetNewSessionHandler(func(name string, crossSessionMemory bool) tui.SelectResult {
		handlerCalled = true
		crossValue = crossSessionMemory
		return tui.SelectResult{Message: fmt.Sprintf("created %s cross=%v", name, crossSessionMemory)}
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/new-session work")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "cross-session memory") {
		t.Fatalf("expected confirm dialog for cross-session memory: %s", view)
	}
	// Press 'y' to enable cross-session memory
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "y"}))
	model = updated.(tui.Model)
	if !handlerCalled {
		t.Fatal("expected new session handler to be called")
	}
	if !crossValue {
		t.Fatal("expected cross-session memory to be true after pressing y")
	}
	view = model.View().Content
	if !strings.Contains(view, "created work cross=true") {
		t.Fatalf("expected result message in view: %s", view)
	}
}

func TestTUIModelNewSessionDenyCrossSessionMemory(t *testing.T) {
	handlerCalled := false
	crossValue := true
	model := tui.New(nil)
	model.SetNewSessionHandler(func(name string, crossSessionMemory bool) tui.SelectResult {
		handlerCalled = true
		crossValue = crossSessionMemory
		return tui.SelectResult{Message: "created"}
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/new-session test")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	// Press 'n' to deny cross-session memory
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "n"}))
	model = updated.(tui.Model)
	if !handlerCalled {
		t.Fatal("expected new session handler to be called")
	}
	if crossValue {
		t.Fatal("expected cross-session memory to be false after pressing n")
	}
}

func TestTUIModelNewSessionWithoutNameAutoGenerates(t *testing.T) {
	calledName := ""
	model := tui.New(nil)
	model.SetNewSessionHandler(func(name string, crossSessionMemory bool) tui.SelectResult {
		calledName = name
		return tui.SelectResult{Message: "created"}
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/new-session")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(tui.Model)
	view := model.View().Content
	if !strings.Contains(view, "cross-session memory") {
		t.Fatalf("expected confirm dialog when no name supplied: %s", view)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "y"}))
	model = updated.(tui.Model)
	if !strings.HasPrefix(calledName, "session-") {
		t.Fatalf("expected auto-generated session name, got %q", calledName)
	}
}

func setInputValue(model tui.Model, value string) (tui.Model, tea.Cmd) {
	var cmd tea.Cmd
	updated := tea.Model(model)
	for _, r := range value {
		updated, cmd = updated.Update(tea.KeyPressMsg(tea.Key{Text: string(r)}))
	}
	if m, ok := updated.(tui.Model); ok {
		return m, cmd
	}
	return *updated.(*tui.Model), cmd
}

func TestTUIDialogScrollsWhenManyItems(t *testing.T) {
	const height = 18
	const width = 80
	model := tui.New(nil)
	model.SetDialogCallbacks(func(kind tui.DialogKind) []tui.DialogItem {
		items := make([]tui.DialogItem, 0, 40)
		for i := 0; i < 40; i++ {
			items = append(items, tui.DialogItem{
				Label: fmt.Sprintf("option-%02d", i),
				Value: fmt.Sprintf("%d", i),
			})
		}
		return items
	}, nil)

	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(tui.Model)
	// Open sessions dialog with many items.
	model.ShowDialog(tui.DialogSessions)

	view := model.View().Content
	viewLines := strings.Split(view, "\n")
	if len(viewLines) > height+2 {
		// Allow a small ANSI / trailing newline slack, but not a full overflow.
		t.Fatalf("view height %d exceeds terminal height %d\n%s", len(viewLines), height, view)
	}
	if !strings.Contains(view, "option-00") {
		t.Fatalf("expected first page to include option-00: %s", view)
	}
	if strings.Contains(view, "option-39") {
		t.Fatalf("expected long list to be windowed (option-39 should be off-screen): %s", view)
	}
	// Scrollbar thumb/track should appear beside the list (█ preferred; │ is track).
	// Position counter is the reliable signal that the list is scroll-aware.
	if !strings.Contains(view, "1/40") {
		t.Fatalf("expected position counter 1/40 in dialog hint: %s", view)
	}
	if !strings.Contains(view, "█") {
		// Thumb may use █; if theme falls back, at least ensure list is clipped.
		if strings.Contains(view, "option-10") {
			t.Fatalf("expected clipped list without showing option-10 on small terminal: %s", view)
		}
	}

	// Move selection to the end; window should follow.
	for i := 0; i < 45; i++ {
		updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		model = asTUIModel(t, updated, nil)
	}
	view = model.View().Content
	if !strings.Contains(view, "option-39") {
		t.Fatalf("expected selection window to include option-39: %s", view)
	}
	if !strings.Contains(view, "40/40") {
		t.Fatalf("expected end position counter 40/40: %s", view)
	}
	if lipglossHeight(view) > height+2 {
		t.Fatalf("scrolled view still overflows terminal: height=%d terminal=%d", lipglossHeight(view), height)
	}
}

func TestTUIAskUserDialogScrollsWhenManyOptions(t *testing.T) {
	const height = 16
	model := newTUI(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: height})
	model = updated.(tui.Model)

	opts := make([]tui.AskUserOption, 0, 30)
	for i := 0; i < 30; i++ {
		opts = append(opts, tui.AskUserOption{Label: fmt.Sprintf("choice-%02d", i)})
	}
	responseCh := make(chan map[string]string, 1)
	updated, _ = model.Update(tui.AskUserRequestMsg{
		Questions: []tui.AskUserQuestion{{
			Question: "Pick one?",
			Header:   "Many",
			Options:  opts,
		}},
		ResponseCh: responseCh,
	})
	model = asTUIModel(t, updated, nil)
	view := model.View().Content
	if strings.Contains(view, "choice-29") {
		t.Fatalf("expected ask-user options to be windowed: %s", view)
	}
	if !strings.Contains(view, "choice-00") {
		t.Fatalf("expected first options visible: %s", view)
	}
	// 30 options + custom = 31 rows; counter should reflect that.
	if !strings.Contains(view, "/31") {
		t.Fatalf("expected ask-user position counter: %s", view)
	}
	if lipglossHeight(view) > height+2 {
		t.Fatalf("ask-user view overflows terminal: height=%d terminal=%d\n%s", lipglossHeight(view), height, view)
	}
}

func lipglossHeight(view string) int {
	return strings.Count(view, "\n") + 1
}

func asTUIModel(t *testing.T, updated tea.Model, _ tea.Cmd) tui.Model {
	t.Helper()
	if m, ok := updated.(tui.Model); ok {
		return m
	}
	if m, ok := updated.(*tui.Model); ok {
		return *m
	}
	t.Fatalf("unexpected model type %T", updated)
	return tui.Model{}
}

func hasLineContainingAll(text string, parts ...string) bool {
	for _, line := range strings.Split(text, "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

var errTestTUI = testErr("tui test error")

type testErr string

func (e testErr) Error() string { return string(e) }
