package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/solosw/solcode/internal/tokenest"
)

const scrollbarWidth = 2
const pasteEnterGuardWindow = 150 * time.Millisecond
const inputBurstWindow = 35 * time.Millisecond
const bulkPasteMinRunes = 8

type SubmitFunc func(prompt string) (tea.Cmd, func())

type StreamTextMsg struct{ Text string }
type StreamThinkingMsg struct{ Text string }
type StreamDoneMsg struct{}
type StreamCanceledMsg struct{ Reason string }
type StreamErrorMsg struct{ Err error }
type StatusTextMsg struct{ Text string }
type CommandResultMsg struct{ Text string }
type ReplaceMessagesMsg struct{ Messages []ChatMessage }

type TokenUsageMsg struct {
	EstimatedContextTokens   int64
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

type AskUserOption struct {
	Label       string
	Description string
	Preview     string
}

type AskUserQuestion struct {
	Question    string
	Header      string
	Options     []AskUserOption
	MultiSelect bool
}

type AskUserRequestMsg struct {
	Questions  []AskUserQuestion
	ResponseCh chan<- map[string]string
}

type ChatMessage struct {
	Role           string
	Content        string
	DisplayContent string
	ToolName       string
	IsError        bool
	Collapsed      bool
	TimeStamp      time.Time
}

type pendingPermission struct {
	toolName    string
	description string
	responseCh  chan<- bool
}

type pendingConfirm struct {
	question string
	resolve  func(bool) SelectResult
}

type pendingAskUser struct {
	questions     []AskUserQuestion
	index         int
	selected      int
	checked       map[int]map[int]bool
	answers       map[string]string
	responseCh    chan<- map[string]string
	customInput   textinput.Model
	editingCustom bool
}

type DialogKind int

const (
	DialogNone DialogKind = iota
	DialogModel
	DialogProvider
	DialogEffort
	DialogSessions
	DialogSkills
	DialogMCP
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
	EstimatedContextTokens   int64
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
	queue    func(string)

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
	pendingConf  *pendingConfirm
	pendingAsk   *pendingAskUser
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
	activeShells   int
	cancelCurrent  func()

	// Input history
	history                []string
	historyIndex           int
	pastedInputLines       int
	lastPasteAt            time.Time
	suppressNextPasteEnter bool
	inputBurstStartedAt    time.Time
	lastInputAt            time.Time
	inputBurstChars        int

	// Select-all mode
	selectAllMode bool

	// Permission mode
	permissionMode    string
	modeNames         []string
	modeSwitchFn      func(mode string)
	slashHandler      SlashCommandHandler
	slashAsyncHandler SlashCommandAsyncHandler
	newSessionHandler NewSessionHandler
	itemsFunc         ModelItemsFunc
	selectFunc        SelectFunc
	skillNamesFn      func() []string
	contextBaseFn     func() int64
	contextLimitFn    func() int64
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
	vp := viewport.New(78, 20)
	input := textarea.New()
	input.Placeholder = "Ask solcode…"
	input.Prompt = ""
	input.Focus()
	input.ShowLineNumbers = false
	input.CharLimit = 20_000
	input.SetHeight(3)
	// Enter submits in the model; Alt+Enter inserts a newline.
	input.KeyMap.InsertNewline = key.NewBinding(key.WithDisabled())
	return Model{
		viewport:      vp,
		input:         input,
		submit:        submit,
		status:        "Ready",
		theme:         theme,
		modelName:     modelName,
		cwd:           cwd,
		showTimestamp: showTimestamp,
		messages:      []ChatMessage{{Role: "welcome", Content: "Welcome to solcode", TimeStamp: time.Now()}},
	}
}

// SetQueueFunc configures delivery of messages submitted while a run is active.
// The callback must return quickly because it is called from the TUI update loop.
func (m *Model) SetQueueFunc(queue func(string)) {
	m.queue = queue
}

func (m *Model) SetSlashCommandHandler(handler SlashCommandHandler) {
	m.slashHandler = handler
}

func (m *Model) SetSlashCommandAsyncHandler(handler SlashCommandAsyncHandler) {
	m.slashAsyncHandler = handler
}

func (m *Model) SetNewSessionHandler(handler NewSessionHandler) {
	m.newSessionHandler = handler
}

func (m *Model) SetDialogCallbacks(itemsFn ModelItemsFunc, selectFn SelectFunc) {
	m.itemsFunc = itemsFn
	m.selectFunc = selectFn
}

func (m *Model) SetModelName(name string) {
	m.modelName = name
}

func (m *Model) SetSkillNamesFn(fn func() []string) {
	m.skillNamesFn = fn
}

func (m *Model) SetModelNameFn(fn func() string) {
	m.modelNameFn = fn
}

func (m *Model) SetContextBaseFn(fn func() int64) {
	m.contextBaseFn = fn
}

func (m *Model) SetContextLimitFn(fn func() int64) {
	m.contextLimitFn = fn
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
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "todowrite", "todolist":
		return false
	}
	return !isFileMutationTool(name)
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
	return tea.Batch(
		textarea.Blink,
		func() tea.Msg { return tea.EnableBracketedPaste() },
	)
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
		if m.pendingConf != nil {
			return m.handleConfirmKey(msg.String())
		}
		if m.pendingAsk != nil {
			return m.handleAskUserKey(msg)
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
		if msg.Type == tea.KeyRunes {
			text := string(msg.Runes)
			if m.likelyBulkPasteRunes(text, msg.Paste) {
				if msg.Paste || strings.ContainsAny(text, "\r\n") || utf8.RuneCountInString(text) >= bulkPasteMinRunes {
					m.pastedInputLines += pastedLineCount(text)
				}
				m.lastPasteAt = time.Now()
				m.suppressNextPasteEnter = true
			} else {
				m.suppressNextPasteEnter = false
			}
		} else {
			m.resetInputBurst()
			if msg.Type != tea.KeyEnter {
				m.suppressNextPasteEnter = false
			}
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
			if !msg.Alt && !msg.Paste {
				if m.suppressNextPasteEnter && time.Since(m.lastPasteAt) <= pasteEnterGuardWindow {
					m.suppressNextPasteEnter = false
					return m, nil
				}
				m.suppressNextPasteEnter = false
				prompt := strings.TrimSpace(m.input.Value())
				if prompt == "" {
					return m, nil
				}
				displayContent := pastedInputDisplay(m.pastedInputLines)
				m.pastedInputLines = 0
				m.saveToHistory(prompt)
				m.input.Reset()
				if m.streaming {
					m.messages = append(m.messages, ChatMessage{Role: "user", Content: prompt, DisplayContent: displayContent, TimeStamp: time.Now()})
					m.status = "Message queued"
					m.refreshViewport()
					if m.queue != nil {
						m.queue(prompt)
					}
					return m, nil
				}
				if handled, cmd := m.handleSlashCommand(prompt); handled {
					return m, cmd
				}
				submitPrompt := prompt
				if skillPrompt, ok := m.slashSkillPrompt(prompt); ok {
					submitPrompt = skillPrompt
				}
				m.messages = append(m.messages, ChatMessage{Role: "user", Content: prompt, DisplayContent: displayContent, TimeStamp: time.Now()})
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
				cmd, cancel := m.submit(submitPrompt)
				m.cancelCurrent = cancel
				return m, tea.Batch(cmd, m.nextSpinnerTick())
			}
		case "alt+enter":
			m.input.InsertString("\n")
			return m, nil
		case "ctrl+shift+c":
			m.copyLastAssistant()
			return m, nil
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
	case StatusTextMsg:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			m.status = "Ready"
		} else {
			m.status = text
		}
		m.refreshViewport()
		return m, nil
	case CommandResultMsg:
		m.spinnerActive = false
		m.loadingStart = time.Time{}
		m.status = "Ready"
		m.appendCommandResult(msg.Text)
		m.refreshViewport()
		return m, nil
	case ReplaceMessagesMsg:
		m.messages = append([]ChatMessage(nil), msg.Messages...)
		m.refreshViewport()
		return m, nil
	case TokenUsageMsg:
		m.tokenUsage = TokenUsage{
			EstimatedContextTokens:   msg.EstimatedContextTokens,
			InputTokens:              msg.InputTokens,
			OutputTokens:             msg.OutputTokens,
			CacheCreationInputTokens: msg.CacheCreationInputTokens,
			CacheReadInputTokens:     msg.CacheReadInputTokens,
			MaxContextTokens:         msg.MaxContextTokens,
		}
		m.refreshViewport()
		return m, nil
	case ToolStartMsg:
		m.startToolActivity(msg)
		m.activeToolName = msg.Name
		if isShellTool(msg.Name) {
			m.activeShells++
			m.status = runningShellsStatus(m.activeShells)
		} else {
			m.status = "Running " + msg.Name
		}
		if m.loadingStart.IsZero() {
			m.loadingStart = time.Now()
		}
		m.resize()
		m.refreshViewport()
		return m, nil
	case ToolDoneMsg:
		m.finishToolActivity(msg)
		if isShellTool(msg.Name) && m.activeShells > 0 {
			m.activeShells--
		}
		if m.activeToolName == msg.Name {
			m.activeToolName = ""
		}
		if m.activeShells > 0 {
			m.status = runningShellsStatus(m.activeShells)
		} else {
			m.status = "Ready"
		}
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
	case AskUserRequestMsg:
		customInput := textinput.New()
		customInput.Placeholder = "Type a custom answer"
		customInput.CharLimit = 1000
		customInput.Width = max(20, m.width-12)
		m.pendingAsk = &pendingAskUser{
			questions:   append([]AskUserQuestion(nil), msg.Questions...),
			checked:     map[int]map[int]bool{},
			answers:     map[string]string{},
			responseCh:  msg.ResponseCh,
			customInput: customInput,
		}
		m.status = "Input required"
		m.resize()
		m.refreshViewport()
		return m, nil
	case AgentStatusMsg:
		m.updateAgentActivity(msg)
		m.resize()
		m.refreshViewport()
		return m, nil
	}

	if m.pendingConf != nil || m.pendingAsk != nil || m.pending != nil || (m.dialog != nil && m.dialog.Active != DialogNone) || m.autocomplete != nil || m.selectAllMode {
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
		// Copy selected text instead of canceling
		m.copyInput()
		m.selectAllMode = false
		return m, nil
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

func pastedLineCount(text string) int {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\r\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func (m *Model) likelyBulkPasteRunes(text string, explicitPaste bool) bool {
	now := time.Now()
	runes := utf8.RuneCountInString(text)
	if runes <= 0 {
		m.resetInputBurst()
		return false
	}
	if explicitPaste {
		m.inputBurstStartedAt = now
		m.lastInputAt = now
		m.inputBurstChars = runes
		return true
	}
	if strings.ContainsAny(text, "\r\n") {
		m.inputBurstStartedAt = now
		m.lastInputAt = now
		m.inputBurstChars += runes
		return true
	}
	if m.lastInputAt.IsZero() || now.Sub(m.lastInputAt) > inputBurstWindow {
		m.inputBurstStartedAt = now
		m.inputBurstChars = runes
	} else {
		m.inputBurstChars += runes
	}
	m.lastInputAt = now
	return runes >= bulkPasteMinRunes || m.inputBurstChars >= bulkPasteMinRunes
}

func (m *Model) resetInputBurst() {
	m.inputBurstStartedAt = time.Time{}
	m.lastInputAt = time.Time{}
	m.inputBurstChars = 0
}

func pastedInputDisplay(lines int) string {
	if lines <= 0 {
		return ""
	}
	if lines == 1 {
		return "Pasted 1 line"
	}
	return fmt.Sprintf("Pasted %d lines", lines)
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
		m.modeNames = []string{"auto", "accept_edits", "bypass", "plan"}
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
	if m.pendingConf != nil {
		m.pendingConf = nil
		m.status = "Ready"
		m.refreshViewport()
		return m, nil
	}
	if m.pendingAsk != nil {
		m.resolveAskUser(nil)
		m.status = "Ready"
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
	commands := []string{"help", "clear", "model", "provider", "effort", "sessions", "compact", "fix-session", "new-session", "skills", "mcp"}
	if m.skillNamesFn != nil {
		commands = append(commands, m.skillNamesFn()...)
	}
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
	m.activeShells = 0
	m.cancelCurrent = nil
	m.stopSpinner()
}

func isShellTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "bash" || name == "shell" || strings.HasSuffix(name, ".bash")
}

func runningShellsStatus(count int) string {
	if count <= 0 {
		return "Ready"
	}
	return fmt.Sprintf("正在运行 %d 个 Shell", count)
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

func (m *Model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	pc := m.pendingConf
	m.pendingConf = nil
	switch strings.ToLower(key) {
	case "y", "enter":
		m.status = "Ready"
		if pc != nil && pc.resolve != nil {
			m.applySelectResult(pc.resolve(true))
		}
		m.resize()
		m.refreshViewport()
		return *m, nil
	case "n", "esc", "ctrl+c":
		m.status = "Ready"
		if pc != nil && pc.resolve != nil {
			m.applySelectResult(pc.resolve(false))
		}
		m.resize()
		m.refreshViewport()
		return *m, nil
	}
	m.pendingConf = pc
	return *m, nil
}

func (m *Model) ShowConfirm(question string, resolve func(bool) SelectResult) {
	m.pendingConf = &pendingConfirm{question: question, resolve: resolve}
	m.status = "Confirm"
	m.resize()
	m.refreshViewport()
}

func (m Model) handleAskUserKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	task := m.pendingAsk
	if task == nil || len(task.questions) == 0 {
		return m, nil
	}
	q := task.questions[task.index]
	key := strings.ToLower(msg.String())
	customIndex := len(q.Options)

	if task.editingCustom {
		switch key {
		case "esc":
			task.editingCustom = false
			task.customInput.Blur()
		case "ctrl+c":
			m.resolveAskUser(nil)
			m.status = "Ready"
		case "enter":
			if strings.TrimSpace(task.customInput.Value()) != "" {
				m.acceptAskUserAnswer()
			}
		default:
			var cmd tea.Cmd
			task.customInput, cmd = task.customInput.Update(msg)
			m.resize()
			m.refreshViewport()
			return m, cmd
		}
		m.resize()
		m.refreshViewport()
		return m, nil
	}

	switch key {
	case "up", "k":
		if task.selected > 0 {
			task.selected--
		}
	case "down", "j":
		if task.selected < customIndex {
			task.selected++
		}
	case " ", "spacebar":
		if q.MultiSelect && task.selected < customIndex {
			if task.checked[task.index] == nil {
				task.checked[task.index] = map[int]bool{}
			}
			task.checked[task.index][task.selected] = !task.checked[task.index][task.selected]
		}
	case "enter":
		if task.selected == customIndex {
			task.editingCustom = true
			task.customInput.Focus()
		} else {
			m.acceptAskUserAnswer()
		}
	case "esc", "ctrl+c":
		m.resolveAskUser(nil)
		m.status = "Ready"
	}
	m.resize()
	m.refreshViewport()
	return m, nil
}

func (m *Model) acceptAskUserAnswer() {
	task := m.pendingAsk
	if task == nil || task.index >= len(task.questions) {
		return
	}
	q := task.questions[task.index]
	answer := ""
	if task.selected == len(q.Options) {
		answer = strings.TrimSpace(task.customInput.Value())
	} else if q.MultiSelect {
		labels := []string{}
		for i, opt := range q.Options {
			if task.checked[task.index] != nil && task.checked[task.index][i] {
				labels = append(labels, opt.Label)
			}
		}
		answer = strings.Join(labels, ", ")
	}
	if answer == "" && len(q.Options) > 0 {
		idx := min(max(task.selected, 0), len(q.Options)-1)
		answer = q.Options[idx].Label
	}
	if task.answers == nil {
		task.answers = map[string]string{}
	}
	task.answers[q.Question] = answer
	if task.index+1 >= len(task.questions) {
		m.resolveAskUser(task.answers)
		m.status = "Ready"
		return
	}
	task.index++
	task.selected = 0
	task.editingCustom = false
	task.customInput.Reset()
	task.customInput.Blur()
	m.status = "Input required"
}

func (m *Model) resolveAskUser(answers map[string]string) {
	if m.pendingAsk == nil {
		return
	}
	responseCh := m.pendingAsk.responseCh
	m.pendingAsk = nil
	if responseCh == nil {
		return
	}
	if answers == nil {
		answers = map[string]string{}
	}
	select {
	case responseCh <- answers:
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
	background := m.theme.BackgroundOverride
	if m.theme.Name == "dark" {
		m.theme = Light
	} else {
		m.theme = Dark
	}
	m.theme = m.theme.WithBackground(background)
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
	parts := []string{m.renderViewportWithScrollbar()}
	if dialog := m.renderActiveDialog(); dialog != "" {
		parts = append(parts, dialog)
	} else if m.autocomplete != nil {
		parts = append(parts, m.renderAutocomplete())
	}
	parts = append(parts, m.renderRuntimeStatusBar(), m.renderStatusBar(), m.renderInput())
	if panel := m.renderActivityPanel(); panel != "" {
		parts = append(parts, panel)
	}
	return lipgloss.NewStyle().Background(m.theme.Background).Width(m.width).Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func (m Model) renderViewportWithScrollbar() string {
	view := m.viewport.View()
	bar := m.renderScrollbar()
	return lipgloss.JoinHorizontal(lipgloss.Top, view, bar)
}

func (m Model) renderScrollbar() string {
	total := m.viewport.TotalLineCount()
	visible := m.viewport.VisibleLineCount()
	height := m.viewport.Height
	if height <= 0 {
		return ""
	}
	trackStyle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	thumbStyle := lipgloss.NewStyle().Foreground(m.theme.Claude)
	if total <= visible {
		var b strings.Builder
		for i := 0; i < height; i++ {
			b.WriteString(trackStyle.Render(" │"))
			if i < height-1 {
				b.WriteString("\n")
			}
		}
		return b.String()
	}
	thumbHeight := max(1, visible*height/total)
	maxThumbPos := total - visible
	thumbPos := 0
	if maxThumbPos > 0 {
		thumbPos = m.viewport.YOffset * (height - thumbHeight) / maxThumbPos
	}
	thumbPos = clamp(thumbPos, 0, height-thumbHeight)
	var b strings.Builder
	for i := 0; i < height; i++ {
		if i >= thumbPos && i < thumbPos+thumbHeight {
			b.WriteString(thumbStyle.Render(" █"))
		} else {
			b.WriteString(trackStyle.Render(" │"))
		}
		if i < height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m Model) renderActiveDialog() string {
	switch {
	case m.pendingConf != nil:
		return m.renderConfirmDialog()
	case m.pendingAsk != nil:
		return m.renderAskUserDialog()
	case m.dialog != nil && m.dialog.Active != DialogNone:
		return m.renderDialog()
	case m.pending != nil:
		return m.renderPermissionDialog()
	}
	return ""
}

func (m Model) renderInput() string {
	t := m.theme

	// Build the input area with proper visual distinction
	inputView := m.input.View()

	// Determine border color — use accent when focused
	borderColor := t.PromptBorder
	if m.input.Focused() {
		borderColor = t.Claude
	}

	// Input area with solid border, background, and padding
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		BorderTop(true).BorderBottom(true).BorderLeft(true).BorderRight(true).
		Padding(0, 1).
		Width(max(1, m.width-2)).
		Background(t.Background)

	// Compose the input without a prompt prefix.
	line := inputView

	// Right-side usage status, replacing the send/newline hint.
	hintText := m.renderUsageStatus()
	if hintText != "" {
		hint := t.Dim.Render(hintText)
		hintWidth := lipgloss.Width(hint)
		contentWidth := m.width - 6 // accounting for border and padding

		// Pad the line so the usage status can sit on the right.
		lineWidth := lipgloss.Width(line)
		gap := max(1, contentWidth-lineWidth-hintWidth)
		line = line + strings.Repeat(" ", gap) + hint
	}

	return inputStyle.Render(line)
}

func (m Model) renderRuntimeStatusBar() string {
	t := m.theme
	label := strings.TrimSpace(m.status)
	if label == "" {
		label = "Ready"
	}
	left := " "
	if m.spinnerActive {
		left += renderSpinnerLabel(t, m.spinnerFrame, label, m.loadingStart)
	} else {
		left += t.Assistant.Render(label)
	}
	if m.activeToolName != "" {
		left += t.Dim.Render(" · ") + t.Tool.Render(m.activeToolName)
	}
	return t.Status.Width(m.width).Render(left)
}

func (m Model) renderStatusBar() string {
	t := m.theme
	modelName := strings.TrimSpace(m.currentModelName())
	if modelName == "" {
		modelName = "solcode"
	}
	left := " " + t.Assistant.Render(modelName)
	rightParts := []string{}
	if m.permissionMode != "" && m.permissionMode != "auto" {
		rightParts = append(rightParts, t.Dim.Render("mode:")+t.ClaudeStyle.Render(m.permissionMode))
	}
	if m.theme.Name != "" {
		rightParts = append(rightParts, m.theme.Name)
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
	used := m.displayContextTokens()
	limit := m.currentContextLimit()
	parts := []string{fmt.Sprintf("ctx %s/%s", compactTokens(used), renderContextLimit(limit))}
	cacheWrite := usage.CacheCreationInputTokens
	cacheRead := usage.CacheReadInputTokens
	if cacheRead > 0 || cacheWrite > 0 {
		cache := fmt.Sprintf("cache %s/%s", compactTokens(cacheRead), compactTokens(cacheWrite))
		if pct := cacheSharePercent(usage.InputTokens, cacheRead, cacheWrite); pct != "" {
			cache += " (" + pct + ")"
		}
		parts = append(parts, cache)
	}
	if usage.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("out %s", compactTokens(usage.OutputTokens)))
	}
	return strings.Join(parts, " · ")
}

func cacheSharePercent(inputTokens, cacheRead, cacheWrite int64) string {
	cacheTotal := cacheRead + cacheWrite
	if cacheTotal <= 0 {
		return ""
	}
	denominator := inputTokens + cacheTotal
	if denominator <= 0 {
		return ""
	}
	percent := cacheTotal * 100 / denominator
	return fmt.Sprintf("%d%%", percent)
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

func renderContextLimit(value int64) string {
	if value <= 0 {
		return "?"
	}
	return compactTokens(value)
}

func (m Model) currentContextLimit() int64 {
	if m.contextLimitFn != nil {
		if value := m.contextLimitFn(); value > 0 {
			return value
		}
	}
	if m.tokenUsage.MaxContextTokens > 0 {
		return m.tokenUsage.MaxContextTokens
	}
	return 0
}

func (m Model) displayContextTokens() int64 {
	if m.tokenUsage.EstimatedContextTokens > 0 {
		input := strings.TrimSpace(m.input.Value())
		if input == "" {
			return m.tokenUsage.EstimatedContextTokens
		}
		return m.tokenUsage.EstimatedContextTokens + int64(tokenest.Text(input))
	}
	return m.localEstimatedContextTokens()
}

func (m Model) localEstimatedContextTokens() int64 {
	base := int64(0)
	if m.contextBaseFn != nil {
		base = m.contextBaseFn()
	}
	var b strings.Builder
	for _, msg := range m.messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		switch role {
		case "tool":
			role = "assistant"
			if strings.TrimSpace(msg.ToolName) != "" {
				content = "[tool use: " + strings.TrimSpace(msg.ToolName) + "]\n" + content
			}
		case "tool-done":
			role = "user"
			content = "[tool result]\n" + content
		case "error", "system", "agent":
			role = "assistant"
		}
		if role != "" {
			b.WriteString(role)
			b.WriteString(": ")
		}
		b.WriteString(content)
		b.WriteString("\n")
	}
	input := strings.TrimSpace(m.input.Value())
	if input != "" {
		b.WriteString("user: ")
		b.WriteString(input)
	}
	return base + int64(tokenest.Text(b.String()))
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
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body))
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
	if kind == DialogEffort {
		title = "Select Effort"
	}
	if kind == DialogSessions {
		title = "Select Session"
	}
	if kind == DialogSkills {
		title = "Toggle Skill"
	}
	if kind == DialogMCP {
		title = "Toggle MCP Server"
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
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render(ErrorMark + "  Permission Required")
	tool := t.Tool.Render(m.pending.toolName)
	desc := truncate(strings.TrimSpace(m.pending.description), 600)
	hint := t.PermHint.Render("[y] Allow   [n] Deny")
	body := strings.Join([]string{title, "", "Tool: " + tool, desc, "", hint}, "\n")
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body))
}

func (m Model) renderConfirmDialog() string {
	t := m.theme
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render("Confirm")
	question := truncate(strings.TrimSpace(m.pendingConf.question), 600)
	hint := t.PermHint.Render("[y] Yes   [n] No")
	body := strings.Join([]string{title, "", question, "", hint}, "\n")
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body))
}

func (m Model) renderAskUserDialog() string {
	t := m.theme
	ask := m.pendingAsk
	if ask == nil || len(ask.questions) == 0 {
		return ""
	}
	q := ask.questions[ask.index]
	titleText := "Question"
	if strings.TrimSpace(q.Header) != "" {
		titleText = q.Header
	}
	if len(ask.questions) > 1 {
		titleText = fmt.Sprintf("%s %d/%d", titleText, ask.index+1, len(ask.questions))
	}
	title := t.PermTitle.Render(titleText)
	lines := []string{title, "", truncate(strings.TrimSpace(q.Question), 600), ""}
	for i, opt := range q.Options {
		marker := "  "
		if i == ask.selected {
			marker = t.ClaudeStyle.Render("❯ ")
		}
		check := ""
		if q.MultiSelect {
			check = "[ ] "
			if ask.checked[ask.index] != nil && ask.checked[ask.index][i] {
				check = "[x] "
			}
		}
		line := marker + check + opt.Label
		if strings.TrimSpace(opt.Description) != "" {
			line += " — " + t.Dim.Render(opt.Description)
		}
		lines = append(lines, line)
		if strings.TrimSpace(opt.Preview) != "" {
			lines = append(lines, "    "+t.Dim.Render(truncate(oneLine(opt.Preview), max(20, m.width-8))))
		}
	}
	customMarker := "  "
	if ask.selected == len(q.Options) {
		customMarker = t.ClaudeStyle.Render("❯ ")
	}
	lines = append(lines, customMarker+"Custom answer", "    "+ask.customInput.View())
	hint := "[↑/↓] Navigate  [Enter] Select or enter custom answer  [Esc] Cancel"
	if q.MultiSelect {
		hint = "[↑/↓] Navigate  [Space] Toggle  [Enter] Submit or enter custom answer  [Esc] Cancel"
	}
	if ask.editingCustom {
		hint = "[Enter] Submit custom answer  [Esc] Back to choices  [Ctrl+C] Cancel"
	}
	lines = append(lines, "", t.PermHint.Render(hint))
	body := strings.Join(lines, "\n")
	dialogWidth := min(60, m.width-4)
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body))
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
	m.viewport.Width = max(1, m.width-scrollbarWidth)
	m.viewport.Height = layout.viewportHeight
	m.input.SetWidth(layout.inputWidth)
	m.input.SetHeight(layout.inputHeight)
	if m.pendingAsk != nil {
		m.pendingAsk.customInput.Width = max(20, m.width-12)
	}
}

func (m Model) layout() tuiLayout {
	inputHeight := 4
	statusHeight := 2 // runtime status line + context/status line
	dialogHeight := m.activeDialogHeight()
	activityHeight := m.activityPanelHeight()
	return tuiLayout{
		viewportHeight: max(1, m.height-inputHeight-statusHeight-dialogHeight-activityHeight),
		inputWidth:     max(1, m.width-4),
		inputHeight:    3,
		statusHeight:   statusHeight,
		dialogHeight:   dialogHeight,
		permHeight:     dialogHeight,
		activityHeight: activityHeight,
		inputY:         max(0, m.height-inputHeight-activityHeight),
		dialogY:        max(0, m.height-inputHeight-statusHeight-dialogHeight-activityHeight),
		permY:          max(0, m.height-activityHeight),
		activityY:      max(0, m.height-activityHeight),
	}
}

func (m Model) activeDialogHeight() int {
	if m.pendingConf != nil || m.pendingAsk != nil || m.pending != nil {
		return 8
	}
	if m.dialog != nil && m.dialog.Active != DialogNone {
		return min(8, len(m.dialog.Items)+4)
	}
	return 0
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
	m.viewport.SetContent(renderMessages(m.messages, m.theme, m.showTimestamp, m.viewport.Width))
	m.viewport.GotoBottom()
}

func (m *Model) copyInput() {
	if err := clipboard.WriteAll(m.input.Value()); err != nil {
		m.status = fmt.Sprintf("Copy failed: %v", err)
		return
	}
	m.status = "Copied input"
}

func (m *Model) copyLastAssistant() {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "assistant" {
			if err := clipboard.WriteAll(m.messages[i].Content); err != nil {
				m.status = fmt.Sprintf("Copy failed: %v", err)
				return
			}
			m.status = "Copied assistant reply"
			return
		}
	}
	m.status = "No assistant reply to copy"
}
