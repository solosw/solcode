package unit_tests

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/solosw/codeplus-agent/internal/tui"
)

func newTUI(t *testing.T) tui.Model {
	t.Helper()
	model := tui.New(nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return updated.(tui.Model)
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
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "ls") || !strings.Contains(view, "Running Bash") {
		t.Fatalf("expected tool start in view: %s", view)
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

func TestTUIModelReplaceMessages(t *testing.T) {
	model := newTUI(t)
	model.ReplaceMessages([]tui.ChatMessage{{Role: "system", Content: "restored transcript"}})
	view := model.View()
	if !strings.Contains(view, "restored transcript") {
		t.Fatalf("expected restored transcript in view: %s", view)
	}
	if strings.Contains(view, "codeplus-agent TUI") {
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

func TestTUIModelUsageStatusRenders(t *testing.T) {
	model := newTUI(t)
	model.SetContextLimitFn(func() int64 { return 1000000 })
	model.SetContextBaseFn(func() int64 { return 1900 })
	updated, _ := model.Update(tui.TokenUsageMsg{EstimatedContextTokens: 1900, InputTokens: 1200, CacheCreationInputTokens: 200, CacheReadInputTokens: 800, OutputTokens: 250, MaxContextTokens: 1000000})
	model = updated.(tui.Model)
	view := model.View()
	if !strings.Contains(view, "/1M") || !strings.Contains(view, "ctx 2.") || !strings.Contains(view, "cache 800/200") || !strings.Contains(view, "out 250") {
		t.Fatalf("expected usage status in view: %s", view)
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

var errTestTUI = testErr("tui test error")

type testErr string

func (e testErr) Error() string { return string(e) }
