package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SubmitFunc func(prompt string) (tea.Cmd, func())

type StreamTextMsg struct{ Text string }
type StreamThinkingMsg struct{ Text string }
type StreamDoneMsg struct{}
type StreamCanceledMsg struct{ Reason string }
type StreamErrorMsg struct{ Err error }

type TokenUsageMsg struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	MaxContextTokens         int64
}

type ToolStartMsg struct {
	Name  string
	Input string
}
type ToolDoneMsg struct {
	Name    string
	Output  string
	IsError bool
}

type AgentStatusMsg struct {
	ID          string
	ParentID    string
	Role        string
	State       string
	Description string
	Output      string
	IsError     bool
}

type PermissionRequestMsg struct {
	ToolName    string
	Description string
	ResponseCh  chan<- bool
}

type ChatMessage struct {
	Role      string
	Content   string
	ToolName  string
	IsError   bool
	Collapsed bool
	TimeStamp time.Time
}

type pendingPermission struct {
	toolName    string
	description string
	responseCh  chan<- bool
}

type DialogKind int

const (
	DialogNone DialogKind = iota
	DialogModel
	DialogProvider
	DialogSessions
)

type DialogItem struct {
	Label    string
	Subtitle string
	Current  bool
	Value    string
}

type DialogState struct {
	Active   DialogKind
	Title    string
	Items    []DialogItem
	Selected int
}

type ModelItemsFunc func(kind DialogKind) []DialogItem

type SelectResult struct {
	Message         string
	Messages        []ChatMessage
	ReplaceMessages bool
}

type SelectFunc func(kind DialogKind, value string) SelectResult

type AutocompleteState struct {
	Items    []string
	Selected int
	Prefix   string
}

type TodoViewItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	ActiveForm string `json:"activeForm"`
}

type ToolActivity struct {
	Name       string
	Summary    string
	State      string
	Output     string
	IsError    bool
	Collapsed  bool
	StartedAt  time.Time
	FinishedAt time.Time
}

type AgentActivity struct {
	ID          string
	ParentID    string
	Role        string
	State       string
	Description string
	Output      string
	IsError     bool
	UpdatedAt   time.Time
}

type TokenUsage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	MaxContextTokens         int64
}

type Model struct {
	viewport viewport.Model
	input    textarea.Model
	submit   SubmitFunc

	messages        []ChatMessage
	status          string
	thinking        string
	streaming       bool
	canceling       bool
	width           int
	height          int
	todos           []TodoViewItem
	agentActivities []AgentActivity
	tokenUsage      TokenUsage

	pending      *pendingPermission
	dialog       *DialogState
	autocomplete *AutocompleteState

	theme          Theme
	modelName      string
	modelNameFn    func() string
	cwd            string
	showTimestamp  bool
	spinnerFrame   int
	spinnerActive  bool
	lastTick       time.Time
	loadingStart   time.Time
	activeToolName string
	cancelCurrent  func()

	// Input history
	history      []string
	historyIndex int

	// Select-all mode
	selectAllMode bool

	// Permission mode
	permissionMode string
	modeNames      []string
	modeSwitchFn   func(mode string)
	slashHandler   SlashCommandHandler
	itemsFunc      ModelItemsFunc
	selectFunc     SelectFunc
}

type spinnerTickMsg time.Time

type tuiLayout struct {
	viewportHeight int
	inputWidth     int
	inputHeight    int
	statusHeight   int
	dialogHeight   int
	permHeight     int
	activityHeight int
	inputY         int
	dialogY        int
	permY          int
	activityY      int
}

func New(submit SubmitFunc) Model {
	return NewWith(submit, Dark, "", "", true)
}

func NewWith(submit SubmitFunc, theme Theme, modelName, cwd string, showTimestamp bool) Model {
	vp := viewport.New(80, 20)
	input := textarea.New()
	input.Placeholder = "Ask codeplus…"
	input.Prompt = ""
	input.Focus()
	input.ShowLineNumbers = false
	input.CharLimit = 20_000
	input.SetHeight(3)
	return Model{
		viewport:      vp,
		input:         input,
		submit:        submit,
		status:        "Ready",
		theme:         theme,
		modelName:     modelName,
		cwd:           cwd,
		showTimestamp: showTimestamp,
		messages:      []ChatMessage{{Role: "system", Content: "codeplus-agent TUI — Enter sends, Alt+Enter newline, Ctrl+C cancels active responses or quits when idle, Ctrl+T toggles theme. Select text normally to copy; scroll chat with PageUp/PageDown or Ctrl+U/Ctrl+D.", TimeStamp: time.Now()}},
	}
}

func (m *Model) SetSlashCommandHandler(handler SlashCommandHandler) {
	m.slashHandler = handler
}

func (m *Model) SetDialogCallbacks(itemsFn ModelItemsFunc, selectFn SelectFunc) {
	m.itemsFunc = itemsFn
	m.selectFunc = selectFn
}

func (m *Model) SetModelName(name string) {
	m.modelName = name
}

func (m *Model) SetModelNameFn(fn func() string) {
	m.modelNameFn = fn
}

func (m *Model) ReplaceMessages(messages []ChatMessage) {
	if m == nil {
		return
	}
	if messages == nil {
		messages = []ChatMessage{}
	}
	m.messages = append([]ChatMessage(nil), messages...)
	m.refreshViewport()
}

func defaultToolCollapsed(name string) bool {
	switch name {
	case "TodoWrite", "TODOList", "TodoList":
		return false
	default:
		return true
	}
}

func agentStatusContent(msg AgentStatusMsg) string {
	label := strings.TrimSpace(msg.Description)
	if label == "" {
		label = strings.TrimSpace(msg.Role)
	}
	if label == "" {
		label = msg.ID
	}

	switch strings.ToLower(strings.TrimSpace(msg.State)) {
	case "running", "started":
		return fmt.Sprintf("Started %s", label)
	case "completed":
		out := strings.TrimSpace(msg.Output)
		if out == "" {
			return fmt.Sprintf("Completed %s", label)
		}
		return fmt.Sprintf("Completed %s\n%s", label, out)
	case "failed":
		out := strings.TrimSpace(msg.Output)
		if out == "" {
			return fmt.Sprintf("Failed %s", label)
		}
		return fmt.Sprintf("Failed %s\n%s", label, out)
	case "cancelled", "canceled":
		return fmt.Sprintf("Canceled %s", label)
	default:
		state := strings.TrimSpace(msg.State)
		if state == "" {
			state = "updated"
		}
		return fmt.Sprintf("%s %s", titleASCII(state), label)
	}
}

func titleASCII(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func (m Model) currentModelName() string {
	if m.modelNameFn != nil {
		return m.modelNameFn()
	}
	return m.modelName
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.refreshViewport()
		return m, nil
	case spinnerTickMsg:
		if m.spinnerActive {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(SpinnerFrames)
			m.lastTick = time.Time(msg)
			return m, tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
		}
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		if m.selectAllMode {
			return m.handleSelectAllKey(msg.String())
		}
		if m.dialog != nil && m.dialog.Active != DialogNone {
			return m.handleDialogKey(msg.String())
		}
		if m.autocomplete != nil {
			return m.handleAutocompleteKey(msg)
		}
		if m.pending != nil {
			return m.handlePermissionKey(msg.String())
		}
		if !m.input.Focused() && msg.Type == tea.KeyRunes {
			m.input.Focus()
		}
		switch msg.String() {
		case "ctrl+c":
			return m.handleCtrlC()
		case "esc":
			return m, tea.Quit
		case "ctrl+t":
			m.toggleTheme()
			m.refreshViewport()
			return m, nil
		case "ctrl+o":
			m.toggleLastToolCollapse()
			m.refreshViewport()
			return m, nil
		case "ctrl+a":
			if m.input.Value() != "" {
				m.selectAllMode = true
			}
			return m, nil
		case "shift+tab":
			m.cyclePermissionMode()
			return m, nil
		case "pgup", "pageup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown", "pagedown":
			m.viewport.PageDown()
			return m, nil
		case "ctrl+u", "shift+up":
			m.viewport.HalfPageUp()
			return m, nil
		case "ctrl+d", "shift+down":
			m.viewport.HalfPageDown()
			return m, nil
		case "up":
			if !m.streaming {
				return m.handleHistoryUp()
			}
		case "down":
			if !m.streaming {
				return m.handleHistoryDown()
			}
		case "enter":
			if !msg.Alt && !m.streaming {
				prompt := strings.TrimSpace(m.input.Value())
				if prompt == "" {
					return m, nil
				}
				m.saveToHistory(prompt)
				m.input.Reset()
				if m.handleSlashCommand(prompt) {
					return m, nil
				}
				m.messages = append(m.messages, ChatMessage{Role: "user", Content: prompt, TimeStamp: time.Now()})
				m.streaming = true
				m.canceling = false
				m.status = "Thinking…"
				m.thinking = ""
				m.activeToolName = ""
				m.startSpinner()
				m.refreshViewport()
				if m.submit == nil {
					return m, func() tea.Msg { return StreamErrorMsg{Err: fmt.Errorf("submit function is not configured")} }
				}
				cmd, cancel := m.submit(prompt)
				m.cancelCurrent = cancel
				return m, tea.Batch(cmd, m.nextSpinnerTick())
			}
		}
	case StreamTextMsg:
		m.appendAssistantDelta(msg.Text)
		if strings.TrimSpace(msg.Text) != "" && m.activeToolName == "" {
			m.status = "Responding…"
		}
		m.refreshViewport()
		return m, nil
	case StreamThinkingMsg:
		m.thinking += msg.Text
		if strings.TrimSpace(m.thinking) != "" {
			m.status = "Thinking… " + truncate(strings.TrimSpace(m.thinking), 80)
		}
		return m, nil
	case StreamDoneMsg:
		m.finishStream("Ready")
		m.refreshViewport()
		return m, nil
	case StreamCanceledMsg:
		reason := strings.TrimSpace(msg.Reason)
		if reason == "" {
			reason = "Canceled"
		}
		m.finishStream(reason)
		m.messages = append(m.messages, m.systemMessage(reason))
		m.refreshViewport()
		return m, nil
	case StreamErrorMsg:
		m.finishStream("Error")
		if msg.Err != nil {
			m.messages = append(m.messages, ChatMessage{Role: "error", Content: msg.Err.Error(), TimeStamp: time.Now()})
		}
		m.refreshViewport()
		return m, nil
	case TokenUsageMsg:
		m.tokenUsage = TokenUsage{
			InputTokens:              msg.InputTokens,
			OutputTokens:             msg.OutputTokens,
			CacheCreationInputTokens: msg.CacheCreationInputTokens,
			CacheReadInputTokens:     msg.CacheReadInputTokens,
			MaxContextTokens:         msg.MaxContextTokens,
		}
		return m, nil
	case ToolStartMsg:
		m.startToolActivity(msg)
		m.activeToolName = msg.Name
		m.status = "Running " + msg.Name
		if m.loadingStart.IsZero() {
			m.loadingStart = time.Now()
		}
		m.resize()
		m.refreshViewport()
		return m, nil
	case ToolDoneMsg:
		m.finishToolActivity(msg)
		if m.activeToolName == msg.Name {
			m.activeToolName = ""
		}
		m.status = "Ready"
		m.resize()
		m.refreshViewport()
		return m, nil
	case PermissionRequestMsg:
		m.pending = &pendingPermission{
			toolName:    msg.ToolName,
			description: msg.Description,
			responseCh:  msg.ResponseCh,
		}
		m.status = "Permission required"
		m.resize()
		m.refreshViewport()
		return m, nil
	case AgentStatusMsg:
		m.updateAgentActivity(msg)
		m.resize()
		m.refreshViewport()
		return m, nil
	}

	if m.pending != nil || (m.dialog != nil && m.dialog.Active != DialogNone) || m.autocomplete != nil || m.selectAllMode {
		return m, nil
	}
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	m.updateAutocomplete()
	return m, tea.Batch(cmds...)
}

// Select-all mode: all text is selected
func (m *Model) handleSelectAllKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "backspace", "delete", "ctrl+h":
		// Delete selected text
		m.input.Reset()
		m.autocomplete = nil
		m.selectAllMode = false
		m.historyIndex = len(m.history)
		return m, nil
	case "ctrl+c":
		m.selectAllMode = false
		return m.handleCtrlC()
	case "esc", "ctrl+a":
		// Exit select-all, keep text
		m.selectAllMode = false
		return m, nil
	case "left", "right", "up", "down", "home", "end":
		// Navigation exits select-all, keep text
		m.selectAllMode = false
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(parseKeyMsg(key))
		return m, cmd
	default:
		// Replace: clear text, then type the new char
		m.input.Reset()
		m.autocomplete = nil
		m.selectAllMode = false
		m.historyIndex = len(m.history)
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(parseKeyMsg(key))
		return m, cmd
	}
}

func parseKeyMsg(key string) tea.KeyMsg {
	switch key {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		return tea.KeyMsg{Type: tea.KeyDelete}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		if len(key) == 1 {
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// Input history navigation
func (m *Model) saveToHistory(prompt string) {
	// Dedup with last entry
	if len(m.history) > 0 && m.history[len(m.history)-1] == prompt {
		return
	}
	m.history = append(m.history, prompt)
	m.historyIndex = len(m.history)
}

func (m *Model) handleHistoryUp() (tea.Model, tea.Cmd) {
	if len(m.history) == 0 {
		return m, nil
	}
	// Clamp historyIndex to valid range
	if m.historyIndex > len(m.history) {
		m.historyIndex = len(m.history)
	}
	// Save current input if at the bottom
	if m.historyIndex == len(m.history) {
		m.history = append(m.history, m.input.Value())
	}
	if m.historyIndex > 0 {
		m.historyIndex--
		m.input.Reset()
		setTextareaValue(&m.input, m.history[m.historyIndex])
	}
	return m, nil
}

func (m *Model) handleHistoryDown() (tea.Model, tea.Cmd) {
	if len(m.history) == 0 || m.historyIndex >= len(m.history) {
		return m, nil
	}
	m.historyIndex++
	if m.historyIndex < len(m.history) {
		m.input.Reset()
		setTextareaValue(&m.input, m.history[m.historyIndex])
	} else {
		// Past the end — clear
		m.input.Reset()
		m.history = m.history[:len(m.history)-1] // remove the saved current input
		m.historyIndex = len(m.history)          // reset to bottom
	}
	return m, nil
}

func setTextareaValue(ta *textarea.Model, value string) {
	ta.SetValue(value)
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Button == tea.MouseButtonWheelUp {
		m.viewport.ScrollUp(3)
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		m.viewport.ScrollDown(3)
		return m, nil
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		layout := m.layout()
		if msg.Y >= layout.inputY && msg.Y < layout.inputY+layout.inputHeight+1 {
			m.input.Focus()
			return m, nil
		}
		m.input.Blur()
		m.autocomplete = nil
		m.selectAllMode = false
		return m, nil
	}
	return m, nil
}

// Permission mode cycling
func (m *Model) cyclePermissionMode() {
	if len(m.modeNames) == 0 {
		m.modeNames = []string{"auto", "accept_edits", "bypass", "yolo", "plan"}
	}
	if m.modeSwitchFn == nil {
		return
	}
	current := m.permissionMode
	if current == "" {
		current = "auto"
	}
	idx := -1
	for i, name := range m.modeNames {
		if name == current {
			idx = i
			break
		}
	}
	next := (idx + 1) % len(m.modeNames)
	m.permissionMode = m.modeNames[next]
	m.modeSwitchFn(m.permissionMode)
	// Show mode switch in messages
	m.appendCommandResult(fmt.Sprintf("Permission mode: %s", m.permissionMode))
	m.refreshViewport()
}

func (m *Model) SetModeSwitchFn(modeNames []string, fn func(mode string)) {
	m.modeNames = modeNames
	m.permissionMode = modeNames[0]
	m.modeSwitchFn = fn
}

func (m Model) handleCtrlC() (tea.Model, tea.Cmd) {
	if m.streaming {
		if m.cancelCurrent != nil && !m.canceling {
			m.cancelCurrent()
		}
		m.canceling = true
		m.status = "Canceling…"
		m.activeToolName = ""
		m.refreshViewport()
		return m, nil
	}
	if m.dialog != nil && m.dialog.Active != DialogNone {
		m.dialog = nil
		m.refreshViewport()
		return m, nil
	}
	return m, tea.Quit
}

func (m Model) handleDialogKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.dialog = nil
		m.refreshViewport()
		return m, nil
	case "ctrl+c":
		m.dialog = nil
		m.refreshViewport()
		return m, nil
	case "up", "k":
		if m.dialog.Selected > 0 {
			m.dialog.Selected--
		}
		return m, nil
	case "down", "j":
		if m.dialog.Selected < len(m.dialog.Items)-1 {
			m.dialog.Selected++
		}
		return m, nil
	case "enter":
		if m.dialog.Selected >= 0 && m.dialog.Selected < len(m.dialog.Items) {
			item := m.dialog.Items[m.dialog.Selected]
			kind := m.dialog.Active
			m.dialog = nil
			result := SelectResult{Message: fmt.Sprintf("Selected: %s", item.Label)}
			if m.selectFunc != nil {
				result = m.selectFunc(kind, item.Value)
			}
			if result.ReplaceMessages {
				m.messages = result.Messages
			}
			if strings.TrimSpace(result.Message) != "" {
				m.messages = append(m.messages, m.systemMessage(result.Message))
			}
			m.status = "Ready"
			m.refreshViewport()
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) handleAutocompleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.autocomplete = nil
		return m, nil
	case "ctrl+c":
		m.autocomplete = nil
		return m.handleCtrlC()
	case "up", "k":
		if m.autocomplete.Selected > 0 {
			m.autocomplete.Selected--
		}
		return m, nil
	case "down", "j":
		if m.autocomplete.Selected < len(m.autocomplete.Items)-1 {
			m.autocomplete.Selected++
		}
		return m, nil
	case "enter", "tab":
		m.applyAutocomplete()
		return m, nil
	case "backspace":
		// let input update, then refresh autocomplete
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.updateAutocomplete()
		return m, cmd
	default:
		// for any other key: update input, then refresh autocomplete
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.updateAutocomplete()
		return m, cmd
	}
}

func (m *Model) applyAutocomplete() {
	if m.autocomplete == nil || m.autocomplete.Selected >= len(m.autocomplete.Items) {
		m.autocomplete = nil
		return
	}
	cmd := m.autocomplete.Items[m.autocomplete.Selected]
	m.input.Reset()
	// set the full command text
	full := "/" + cmd + " "
	for _, r := range full {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.autocomplete = nil
}

func (m *Model) updateAutocomplete() tea.Cmd {
	value := m.input.Value()
	if !strings.HasPrefix(value, "/") || strings.Contains(value, " ") {
		m.autocomplete = nil
		return nil
	}
	prefix := strings.TrimPrefix(value, "/")
	commands := []string{"help", "clear", "model", "provider", "sessions", "new-session"}
	var matches []string
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, prefix) && cmd != prefix {
			matches = append(matches, cmd)
		}
	}
	if len(matches) == 0 {
		m.autocomplete = nil
		return nil
	}
	m.autocomplete = &AutocompleteState{
		Items:    matches,
		Selected: 0,
		Prefix:   prefix,
	}
	return nil
}

func (m *Model) finishStream(status string) {
	m.streaming = false
	m.canceling = false
	m.status = status
	m.thinking = ""
	m.activeToolName = ""
	m.cancelCurrent = nil
	m.stopSpinner()
}

func (m Model) handlePermissionKey(key string) (tea.Model, tea.Cmd) {
	switch strings.ToLower(key) {
	case "y":
		m.resolvePermission(true)
		m.status = "Ready"
		m.resize()
		m.refreshViewport()
		return m, nil
	case "n", "esc":
		m.resolvePermission(false)
		m.status = "Ready"
		m.resize()
		m.refreshViewport()
		return m, nil
	case "ctrl+c":
		m.resolvePermission(false)
		if m.cancelCurrent != nil && !m.canceling {
			m.cancelCurrent()
		}
		m.canceling = true
		m.status = "Canceling…"
		m.resize()
		m.refreshViewport()
		return m, nil
	}
	return m, nil
}

func (m *Model) resolvePermission(allowed bool) {
	if m.pending == nil || m.pending.responseCh == nil {
		m.pending = nil
		return
	}
	responseCh := m.pending.responseCh
	m.pending = nil
	select {
	case responseCh <- allowed:
	default:
	}
}

func (m *Model) startSpinner() {
	if !m.spinnerActive {
		m.spinnerFrame = 0
	}
	m.spinnerActive = true
	m.loadingStart = time.Now()
}

func (m Model) nextSpinnerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
}

func (m *Model) stopSpinner() {
	m.spinnerActive = false
	m.loadingStart = time.Time{}
}

func (m *Model) toggleTheme() {
	if m.theme.Name == "dark" {
		m.theme = Light
	} else {
		m.theme = Dark
	}
}

func (m *Model) toggleLastToolCollapse() {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "tool-done" || m.messages[i].Role == "tool" {
			m.messages[i].Collapsed = !m.messages[i].Collapsed
			return
		}
	}
}

func (m *Model) startToolActivity(msg ToolStartMsg) {
	m.messages = append(m.messages, ChatMessage{
		Role:      "tool",
		ToolName:  msg.Name,
		Content:   msg.Input,
		Collapsed: true,
		TimeStamp: time.Now(),
	})
	m.updateTodosFromToolInput(msg)
}

func (m *Model) finishToolActivity(msg ToolDoneMsg) {
	m.messages = append(m.messages, ChatMessage{
		Role:      "tool-done",
		ToolName:  msg.Name,
		Content:   msg.Output,
		IsError:   msg.IsError,
		Collapsed: defaultToolCollapsed(msg.Name),
		TimeStamp: time.Now(),
	})
}

func (m *Model) updateTodosFromToolInput(msg ToolStartMsg) {
	switch msg.Name {
	case "TodoWrite", "TODOList", "TodoList":
	default:
		return
	}
	var payload struct {
		Todos []TodoViewItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(msg.Input), &payload); err != nil {
		return
	}
	allDone := len(payload.Todos) > 0
	for _, todo := range payload.Todos {
		if strings.ToLower(strings.TrimSpace(todo.Status)) != "completed" {
			allDone = false
			break
		}
	}
	if allDone {
		m.todos = nil
		return
	}
	m.todos = append([]TodoViewItem(nil), payload.Todos...)
}

func (m *Model) updateAgentActivity(msg AgentStatusMsg) {
	now := time.Now()
	activity := AgentActivity{
		ID:          msg.ID,
		ParentID:    msg.ParentID,
		Role:        msg.Role,
		State:       msg.State,
		Description: msg.Description,
		Output:      msg.Output,
		IsError:     msg.IsError,
		UpdatedAt:   now,
	}
	for i := range m.agentActivities {
		if m.agentActivities[i].ID == msg.ID {
			m.agentActivities[i] = activity
			return
		}
	}
	m.agentActivities = append(m.agentActivities, activity)
	if len(m.agentActivities) > 8 {
		m.agentActivities = m.agentActivities[len(m.agentActivities)-8:]
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "initializing..."
	}
	parts := []string{m.viewport.View()}
	if m.autocomplete != nil {
		parts = append(parts, m.renderAutocomplete())
	}
	parts = append(parts, m.renderStatusBar(), m.renderInput())
	if m.dialog != nil && m.dialog.Active != DialogNone {
		parts = append(parts, m.renderDialog())
	}
	if m.pending != nil {
		parts = append(parts, m.renderPermissionDialog())
	}
	if panel := m.renderActivityPanel(); panel != "" {
		parts = append(parts, panel)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) renderInput() string {
	t := m.theme
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.PromptBorder).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		PaddingTop(0).
		Width(max(1, m.width))
	prompt := t.Prompt.Render(PromptPrefix)
	return border.Render(prompt + m.input.View())
}

func (m Model) renderStatusBar() string {
	t := m.theme
	modelName := strings.TrimSpace(m.currentModelName())
	if modelName == "" {
		modelName = "codeplus"
	}
	left := " " + t.Assistant.Render(modelName)
	if m.spinnerActive {
		left = " " + renderSpinnerLabel(t, m.spinnerFrame, m.status, m.loadingStart)
	} else if m.status != "" {
		left += t.Dim.Render(" · ") + m.status
	}
	if m.activeToolName != "" {
		left += t.Dim.Render(" · ") + t.Tool.Render(m.activeToolName)
	}
	rightParts := []string{}
	if m.permissionMode != "" && m.permissionMode != "auto" {
		rightParts = append(rightParts, t.Dim.Render("mode:")+t.ClaudeStyle.Render(m.permissionMode))
	}
	if m.theme.Name != "" {
		rightParts = append(rightParts, m.theme.Name)
	}
	if usage := m.renderUsageStatus(); usage != "" {
		rightParts = append(rightParts, t.ClaudeStyle.Render(usage))
	}
	if m.cwd != "" {
		rightParts = append(rightParts, truncate(m.cwd, 40))
	}
	right := ""
	if len(rightParts) > 0 {
		right = strings.Join(rightParts, " · ")
	}
	gap := strings.Repeat(" ", max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right)-2))
	bar := left + gap + right
	return t.Status.Width(m.width).Render(bar)
}

func (m Model) renderUsageStatus() string {
	usage := m.tokenUsage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
		return ""
	}
	cacheWrite := usage.CacheCreationInputTokens
	cacheRead := usage.CacheReadInputTokens
	contextTokens := usage.InputTokens + cacheWrite + cacheRead
	parts := []string{}
	if usage.MaxContextTokens > 0 {
		parts = append(parts, fmt.Sprintf("ctx %s/%s", compactTokens(contextTokens), compactTokens(usage.MaxContextTokens)))
	}
	if cacheRead > 0 || cacheWrite > 0 {
		parts = append(parts, fmt.Sprintf("cache %s/%s", compactTokens(cacheRead), compactTokens(cacheWrite)))
	}
	if usage.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("out %s", compactTokens(usage.OutputTokens)))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("in %s", compactTokens(usage.InputTokens)))
	}
	return strings.Join(parts, " · ")
}

func compactTokens(value int64) string {
	if value >= 1_000_000 {
		if value%1_000_000 == 0 {
			return fmt.Sprintf("%dM", value/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	}
	if value >= 1_000 {
		if value%1_000 == 0 {
			return fmt.Sprintf("%dk", value/1_000)
		}
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	}
	return fmt.Sprintf("%d", value)
}

func (m Model) renderAutocomplete() string {
	t := m.theme
	var b strings.Builder
	b.WriteString("  " + t.Dim.Render("Commands:") + "\n")
	for i, item := range m.autocomplete.Items {
		if i == m.autocomplete.Selected {
			b.WriteString("  " + t.ClaudeStyle.Render("❯ /"+item) + "\n")
		} else {
			b.WriteString("    " + t.Dim.Render("/"+item) + "\n")
		}
	}
	return b.String()
}

func (m Model) renderDialog() string {
	t := m.theme
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render(m.dialog.Title)
	var items strings.Builder
	for i, item := range m.dialog.Items {
		prefix := "  "
		if i == m.dialog.Selected {
			prefix = t.ClaudeStyle.Render("❯ ") + t.ClaudeStyle.Render(item.Label)
		} else {
			prefix += item.Label
		}
		if item.Current {
			prefix += t.Dim.Render(" (current)")
		}
		if item.Subtitle != "" {
			prefix += "  " + t.Dim.Render(item.Subtitle)
		}
		items.WriteString(prefix + "\n")
	}
	hint := t.PermHint.Render("[↑/↓] Navigate  [Enter] Select  [Esc] Cancel")
	body := strings.Join([]string{title, "", items.String(), hint}, "\n")
	return t.PermBorder.Width(dialogWidth).Render(body)
}

func (m *Model) ShowDialog(kind DialogKind) {
	if m.itemsFunc == nil {
		return
	}
	items := m.itemsFunc(kind)
	if len(items) == 0 {
		if kind == DialogSessions {
			m.appendCommandResult("No saved sessions yet. Use /new-session [name] to create one.")
		} else {
			m.appendCommandResult("No items available.")
		}
		return
	}
	title := "Select Model"
	if kind == DialogProvider {
		title = "Select Provider"
	}
	if kind == DialogSessions {
		title = "Select Session"
	}
	m.dialog = &DialogState{
		Active:   kind,
		Title:    title,
		Items:    items,
		Selected: 0,
	}
	m.refreshViewport()
}

func (m Model) renderPermissionDialog() string {
	t := m.theme
	title := t.PermTitle.Render(ErrorMark + "  Permission Required")
	tool := t.Tool.Render(m.pending.toolName)
	desc := truncate(strings.TrimSpace(m.pending.description), 600)
	hint := t.PermHint.Render("[y] Allow   [n] Deny")
	body := strings.Join([]string{title, "", "Tool: " + tool, desc, "", hint}, "\n")
	return t.PermBorder.Width(max(1, m.width-2)).Render(body)
}

func (m Model) renderActivityPanel() string {
	sections := []string{}
	if panel := m.renderTodoPanel(); panel != "" {
		sections = append(sections, panel)
	}
	if panel := m.renderAgentPanel(); panel != "" {
		sections = append(sections, panel)
	}
	if len(sections) == 0 {
		return ""
	}
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m Model) renderTodoPanel() string {
	if len(m.todos) == 0 {
		return ""
	}
	t := m.theme
	limit := min(3, len(m.todos))
	lines := []string{" " + t.ClaudeStyle.Render("Todos")}
	for _, todo := range m.todos[:limit] {
		marker := "[ ]"
		switch strings.ToLower(todo.Status) {
		case "in_progress":
			marker = "[→]"
		case "completed":
			marker = "[✓]"
		}
		lines = append(lines, "  "+marker+" "+truncate(oneLine(todo.Content), max(20, m.width-10)))
	}
	if len(m.todos) > limit {
		lines = append(lines, "  "+t.Dim.Render(fmt.Sprintf("+%d more", len(m.todos)-limit)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderAgentPanel() string {
	if len(m.agentActivities) == 0 {
		return ""
	}
	t := m.theme
	limit := min(2, len(m.agentActivities))
	start := len(m.agentActivities) - limit
	lines := []string{" " + t.Assistant.Render("Agents")}
	for _, activity := range m.agentActivities[start:] {
		line := "  " + oneLine(agentStatusContent(AgentStatusMsg{
			ID:          activity.ID,
			ParentID:    activity.ParentID,
			Role:        activity.Role,
			State:       activity.State,
			Description: activity.Description,
			Output:      activity.Output,
			IsError:     activity.IsError,
		}))
		lines = append(lines, truncate(line, max(20, m.width-4)))
		if activity.Output != "" {
			lines = append(lines, "    "+truncate(oneLine(activity.Output), max(20, m.width-8)))
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) activityPanelHeight() int {
	height := 0
	if len(m.todos) > 0 {
		height += min(4, len(m.todos)+1)
	}
	if len(m.agentActivities) > 0 {
		height += min(3, len(m.agentActivities)+1)
	}
	return min(6, height)
}

func (m *Model) resize() {
	layout := m.layout()
	m.viewport.Width = max(1, m.width)
	m.viewport.Height = layout.viewportHeight
	m.input.SetWidth(layout.inputWidth)
	m.input.SetHeight(layout.inputHeight)
}

func (m Model) layout() tuiLayout {
	inputHeight := 4
	statusHeight := 1
	dialogHeight := 0
	if m.dialog != nil && m.dialog.Active != DialogNone {
		dialogHeight = min(8, len(m.dialog.Items)+4)
	}
	permHeight := 0
	if m.pending != nil {
		permHeight = 8
	}
	activityHeight := m.activityPanelHeight()
	return tuiLayout{
		viewportHeight: max(1, m.height-inputHeight-statusHeight-dialogHeight-permHeight-activityHeight),
		inputWidth:     max(1, m.width-4),
		inputHeight:    3,
		statusHeight:   statusHeight,
		dialogHeight:   dialogHeight,
		permHeight:     permHeight,
		activityHeight: activityHeight,
		inputY:         max(0, m.height-inputHeight-dialogHeight-permHeight-activityHeight),
		dialogY:        max(0, m.height-dialogHeight-permHeight-activityHeight),
		permY:          max(0, m.height-permHeight-activityHeight),
		activityY:      max(0, m.height-activityHeight),
	}
}

func (m *Model) appendAssistantDelta(text string) {
	if text == "" {
		return
	}
	last := len(m.messages) - 1
	if last < 0 || m.messages[last].Role != "assistant" {
		m.messages = append(m.messages, ChatMessage{Role: "assistant", TimeStamp: time.Now()})
		last = len(m.messages) - 1
	}
	m.messages[last].Content += text
}

func (m *Model) refreshViewport() {
	m.viewport.SetContent(renderMessages(m.messages, m.theme, m.showTimestamp, m.width))
	m.viewport.GotoBottom()
}
