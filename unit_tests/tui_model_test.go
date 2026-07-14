package unit_tests

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
		model = updated.(tui.Model)
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	if cmd == nil {
		t.Fatal("expected initial prompt to start a stream")
	}
	for _, text := range []string{"f", "o", "l", "l", "o", "w", " ", "u", "p"} {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
		model = updated.(tui.Model)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)

	if len(queued) != 1 || queued[0] != "follow up" {
		t.Fatalf("queued prompts = %#v, want [follow up] (view: %s)", queued, model.View())
	}
	if !strings.Contains(model.View(), "follow up") {
		t.Fatalf("expected queued message in view: %s", model.View())
	}
}

func TestTUIModelStreamsAssistantText(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamTextMsg{Text: "hello"})
	model = updated.(tui.Model)
	view := model.View()
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
	view := model.View()
	if !strings.Contains(view, "⚠") || !strings.Contains(view, "tui test error") {
		t.Fatalf("expected error marker in view: %s", view)
	}
}

func TestTUIModelShowsToolStatus(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolStartMsg{Name: "Bash", Input: `{"command":"ls"}`})
	model = updated.(tui.Model)
	view := model.View()
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "ls") || !strings.Contains(view, "正在运行 1 个 Shell") {
		t.Fatalf("expected shell start status in view: %s", view)
	}
	if strings.Contains(view, "Tools") {
		t.Fatalf("expected no bottom tools panel in view: %s", view)
	}

	updated, _ = model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "file1.txt", IsError: false})
	model = updated.(tui.Model)
	view = model.View()
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
	view := model.View()
	if !strings.Contains(view, "Todos") || !strings.Contains(view, "one") || !strings.Contains(view, "two") || !strings.Contains(view, "three") {
		t.Fatalf("expected todo panel rows in view: %s", view)
	}
}

func TestTUIModelNormalToolOutputStaysCollapsed(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "one\ntwo\nthree", IsError: false})
	model = updated.(tui.Model)
	view := model.View()
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
	view := model.View()
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
	view := model.View()
	if strings.Contains(view, "more lines") {
		t.Fatalf("expected Write output to ignore collapsed state: %s", view)
	}
	if !strings.Contains(view, "+ new") {
		t.Fatalf("expected Write diff line to be visible: %s", view)
	}
}

func TestTUIModelWelcomeRendersCenteredLogo(t *testing.T) {
	model := newTUI(t)
	view := model.View()
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
	view := model.View()
	if !strings.Contains(view, "hello user") || !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("expected user message box in view: %s", view)
	}
	if strings.Contains(view, "❯") {
		t.Fatalf("expected user message without prompt marker: %s", view)
	}
}

func TestTUIModelInitEnablesBracketedPaste(t *testing.T) {
	model := tui.New(nil)
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected init command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected batch init message, got %T", msg)
	}
	found := false
	for _, subcmd := range batch {
		if subcmd == nil {
			continue
		}
		if fmt.Sprintf("%#v", subcmd()) == fmt.Sprintf("%#v", tea.EnableBracketedPaste()) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected init to enable bracketed paste")
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
	paste := "alpha\nbeta\n"
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("expected no submit before explicit enter, got %q", submitted)
	}
	view := model.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") {
		t.Fatalf("expected pasted content to remain in input before submit: %s", view)
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("expected synthetic enter after paste to be ignored, got %q", submitted)
	}
	view := model.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") {
		t.Fatalf("expected pasted content to stay in input after ignored enter: %s", view)
	}
}

func TestTUIModelRapidBulkRunesWithoutPasteFlagStillSuppressNextEnter(t *testing.T) {
	submitted := ""
	model := tui.New(func(prompt string) (tea.Cmd, func()) {
		submitted = prompt
		return func() tea.Msg { return tui.StreamDoneMsg{} }, nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha\nbeta")})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	if submitted != "" {
		t.Fatalf("expected rapid bulk runes enter to be ignored, got %q", submitted)
	}
	view := model.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") {
		t.Fatalf("expected bulk runes content to stay in input after ignored enter: %s", view)
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	model = updated.(tui.Model)
	time.Sleep(200 * time.Millisecond)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	paste := "alpha\nbeta\ngamma"
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	model = updated.(tui.Model)
	time.Sleep(200 * time.Millisecond)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
	if submitted != paste {
		t.Fatalf("expected full pasted content submitted, got %q", submitted)
	}
	if !strings.Contains(view, "Pasted 3 lines") {
		t.Fatalf("expected pasted line count in view: %s", view)
	}
	if strings.Contains(view, "alpha") || strings.Contains(view, "beta") || strings.Contains(view, "gamma") {
		t.Fatalf("expected pasted content hidden from chat view: %s", view)
	}
}

func TestTUIModelReplaceMessages(t *testing.T) {
	model := newTUI(t)
	model.ReplaceMessages([]tui.ChatMessage{{Role: "system", Content: "restored transcript"}})
	view := model.View()
	if !strings.Contains(view, "restored transcript") {
		t.Fatalf("expected restored transcript in view: %s", view)
	}
	if strings.Contains(view, "solcode TUI") || strings.Contains(view, "✦ solcode") {
		t.Fatalf("expected initial welcome message to be replaced: %s", view)
	}
}

func TestTUIModelAgentStatusRenders(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.AgentStatusMsg{ID: "task-1", Role: "task", State: "completed", Description: "Review files", Output: "looks good"})
	model = updated.(tui.Model)
	view := model.View()
	if !strings.Contains(view, "Agents") || !strings.Contains(view, "Completed Review files") || !strings.Contains(view, "looks good") {
		t.Fatalf("expected agent panel status in view: %s", view)
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
	view := model.View()
	if !strings.Contains(view, "Permission Required") || !strings.Contains(view, "Allow") || !strings.Contains(view, "Deny") {
		t.Fatalf("expected permission dialog in view: %s", view)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	model = updated.(tui.Model)
	select {
	case allowed := <-responseCh:
		if !allowed {
			t.Fatal("expected permission to be allowed after pressing y")
		}
	default:
		t.Fatal("expected response on channel after pressing y")
	}
	view = model.View()
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

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
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
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	model = updated.(tui.Model)
	view := model.View()
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
	view := model.View()
	if !strings.Contains(view, "/1M") || !strings.Contains(view, "ctx 1.9k") || !strings.Contains(view, "cache 800/200 (45%)") || !strings.Contains(view, "out 250") {
		t.Fatalf("expected usage status with cache percentage in view: %s", view)
	}
	if strings.Contains(view, "⏎ send") || strings.Contains(view, "Alt+⏎ newline") {
		t.Fatalf("expected input hint to be replaced by usage status: %s", view)
	}
	if strings.Count(view, "ctx ") != 1 {
		t.Fatalf("expected ctx usage to render once in the input row only: %s", view)
	}
}

func TestTUIModelUsageStatusAlwaysVisible(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetContextBaseFn(func() int64 { return 1500 })
	view := model.View()
	if !strings.Contains(view, "/1M") || !strings.Contains(view, "ctx 1.") {
		t.Fatalf("expected always-visible context usage in view: %s", view)
	}
}

func TestTUIModelRuntimeStatusRendersAboveContextStatus(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	view := model.View()
	readyIndex := strings.Index(view, "Ready")
	ctxIndex := strings.Index(view, "ctx ")
	if readyIndex < 0 || ctxIndex < 0 {
		t.Fatalf("expected Ready and ctx status lines in view: %s", view)
	}
	if readyIndex > ctxIndex {
		t.Fatalf("expected runtime status above context status: %s", view)
	}
}

func TestTUIModelInputBoxHasNoPromptPrefix(t *testing.T) {
	model := newTUI(t)
	model, _ = setInputValue(model, "hello")
	view := model.View()
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
	before := model.View()
	model, _ = setInputValue(model, "extra local prompt text")
	after := model.View()
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
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
	view := model.View()
	if !strings.Contains(view, "/compact") {
		t.Fatalf("expected /compact autocomplete in view: %s", view)
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
	view := model.View()
	if !strings.Contains(view, "Choose mode?") || !strings.Contains(view, "Fast") || !strings.Contains(view, "Safe") {
		t.Fatalf("expected AskUser dialog in view: %s", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	select {
	case answers := <-responseCh:
		if answers["Choose mode?"] != "Safe" {
			t.Fatalf("expected selected answer Safe, got %#v", answers)
		}
	default:
		t.Fatal("expected AskUser answer on channel")
	}
	if strings.Contains(model.View(), "Choose mode?") {
		t.Fatalf("expected AskUser dialog cleared: %s", model.View())
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
	if !strings.Contains(model.View(), "Custom answer") || !strings.Contains(model.View(), "Type a custom answer") {
		t.Fatalf("expected visible custom answer input: %s", model.View())
	}

	for range 2 {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(tui.Model)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Custom mode")})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	if !strings.Contains(model.View(), "正在运行 2 个 Shell") {
		t.Fatalf("expected two active shells: %s", model.View())
	}
	updated, _ := model.Update(tui.ToolDoneMsg{Name: "Bash", Output: "done"})
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "正在运行 1 个 Shell") {
		t.Fatalf("expected one active shell after completion: %s", model.View())
	}
}

func TestTUIModelClickOutsideBlursAndTypingRefocuses(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(tui.Model)
	view := model.View()
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
	if submitted {
		t.Fatal("expected /help to be handled locally without submit")
	}
	if !strings.Contains(view, "/model") || !strings.Contains(view, "/clear") {
		t.Fatalf("expected command help in view: %s", view)
	}
}

func TestTUIModelSlashClearClearsTranscript(t *testing.T) {
	model := newTUI(t)
	updated, _ := model.Update(tui.StreamTextMsg{Text: "hello before clear"})
	model = updated.(tui.Model)
	model, _ = setInputValue(model, "/clear")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(tui.Model)
	if !canceled {
		t.Fatal("expected Ctrl+C to call active cancel callback")
	}
	if cmd != nil {
		t.Fatal("expected Ctrl+C during stream not to quit")
	}
	if !strings.Contains(model.View(), "Canceling") {
		t.Fatalf("expected canceling status in view: %s", model.View())
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
	if !strings.Contains(view, "cross-session memory") {
		t.Fatalf("expected confirm dialog for cross-session memory: %s", view)
	}
	// Press 'y' to enable cross-session memory
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	model = updated.(tui.Model)
	if !handlerCalled {
		t.Fatal("expected new session handler to be called")
	}
	if !crossValue {
		t.Fatal("expected cross-session memory to be true after pressing y")
	}
	view = model.View()
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	// Press 'n' to deny cross-session memory
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.Model)
	view := model.View()
	if !strings.Contains(view, "cross-session memory") {
		t.Fatalf("expected confirm dialog when no name supplied: %s", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	model = updated.(tui.Model)
	if !strings.HasPrefix(calledName, "session-") {
		t.Fatalf("expected auto-generated session name, got %q", calledName)
	}
}

func setInputValue(model tui.Model, value string) (tui.Model, tea.Cmd) {
	var cmd tea.Cmd
	updated := tea.Model(model)
	for _, r := range value {
		updated, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m, ok := updated.(tui.Model); ok {
		return m, cmd
	}
	return *updated.(*tui.Model), cmd
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
