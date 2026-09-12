package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/tokenest"
)

const scrollbarWidth = 2
const pasteEnterGuardWindow = 150 * time.Millisecond
const foldedPasteMinChars = 1000
const foldedPasteMinLines = 5
const maxHistoryEntries = 500
const maxTUIMessages = 800

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
	// SessionTotals, when true, replaces cumulative counters with absolute
	// session values (loaded/persisted totals). When false, values are treated
	// as per-request deltas and accumulated.
	SessionTotals bool
}

type ToolStartMsg struct {
	Name      string
	Input     string
	ToolUseID string
}
type ToolDoneMsg struct {
	Name      string
	Output    string
	IsError   bool
	ToolUseID string
}

type AgentStatusMsg struct {
	ID              string
	ParentID        string
	ParentToolUseID string
	TaskID          string
	Role            string
	State           string
	Description     string
	ToolName        string
	Output          string
	IsError         bool
}

type AgentProgressMsg struct {
	Kind            string
	AgentID         string
	ParentID        string
	ParentToolUseID string
	TaskID          string
	Description     string
	ToolName        string
	ToolInput       string
	Output          string
	IsError         bool
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
	ToolUseID      string
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
	Custom   bool
}

type DialogState struct {
	Active       DialogKind
	Title        string
	Items        []DialogItem
	Selected     int
	Custom       bool
	CustomStep   int
	CustomValues []string
	CustomInput  textinput.Model
}

type ModelItemsFunc func(kind DialogKind) []DialogItem

type SelectResult struct {
	Message         string
	Messages        []ChatMessage
	ReplaceMessages bool
	// TokenUsage, when set, replaces session token totals after message replace
	// (used when switching/loading a persisted session).
	TokenUsage *TokenUsageMsg
}

type SelectFunc func(kind DialogKind, value string) SelectResult
type CustomSelectFunc func(kind DialogKind, values []string) SelectResult

type AutocompleteKind int

const (
	AutocompleteSlash AutocompleteKind = iota
	AutocompleteFile
)

type AutocompleteState struct {
	Items    []string
	Selected int
	Prefix   string
	Kind     AutocompleteKind
	// AtStart is the byte index of the '@' token start in the input (file mode).
	AtStart int
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
	ID              string
	ParentID        string
	ParentToolUseID string
	TaskID          string
	Role            string
	State           string
	Description     string
	ToolName        string
	Output          string
	IsError         bool
	// ProcessLines are live execution lines for this subagent panel.
	ProcessLines []string
	UpdatedAt    time.Time
}

type TokenUsage struct {
	EstimatedContextTokens   int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	MaxContextTokens         int64
}

type pastedBlock struct {
	label string
	text  string
}

type Model struct {
	viewport viewport.Model
	input    textarea.Model
	submit   SubmitFunc
	queue    func(string)

	pastedBlocks []pastedBlock
	nextPasteID  int

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

	pending                 *pendingPermission
	pendingConf             *pendingConfirm
	pendingAsk              *pendingAskUser
	dialog                  *DialogState
	autocomplete            *AutocompleteState
	workflowEditor          *WorkflowEditorState
	workflowEditorCallbacks *WorkflowEditorCallbacks
	workflowUIHandler       func() string

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

	// Input history (capped at maxHistoryEntries)
	history      []string
	historyIndex int
	lastPasteAt  time.Time

	// render cache: avoids re-rendering the full transcript on every spinner tick
	renderedContent        string
	renderedMsgVersion     int
	renderedCachedVersion  int
	renderedWidth          int
	suppressNextPasteEnter bool

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
	customSelectFunc  CustomSelectFunc
	skillNamesFn      func() []string
	workflowNamesFn   func() []string
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
	vp := viewport.New(viewport.WithWidth(78), viewport.WithHeight(20))
	// Transcript navigation is handled explicitly by Model.Update. The v2
	// viewport defaults bind ordinary letters such as h/l/j/k/u/d/f/b, which
	// otherwise also scroll the transcript while the user types or drags a path.
	vp.KeyMap = viewport.KeyMap{}
	input := textarea.New()
	input.Placeholder = "Ask solcode…"
	input.Prompt = ""
	input.Focus()
	input.ShowLineNumbers = false
	input.CharLimit = 20_000
	input.DynamicHeight = false
	input.MinHeight = 2
	input.MaxHeight = 2
	input.SetHeight(2)
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

func (m *Model) SetCustomDialogCallback(fn CustomSelectFunc) {
	m.customSelectFunc = fn
}

func (m *Model) SetModelName(name string) {
	m.modelName = name
}

func (m *Model) SetSkillNamesFn(fn func() []string) {
	m.skillNamesFn = fn
}

// SetWorkflowNamesFn supplies loaded workflow names for direct slash invoke.
// Each workflow "name" is exposed as /name-workflow (suffix added if missing).
func (m *Model) SetWorkflowNamesFn(fn func() []string) {
	m.workflowNamesFn = fn
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

// ApplyTokenUsage applies a usage update outside the tea message loop
// (e.g. restoring persisted totals during TUI bootstrap).
func (m *Model) ApplyTokenUsage(msg TokenUsageMsg) {
	if m == nil {
		return
	}
	m.applyTokenUsage(msg)
}

func defaultToolCollapsed(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "todowrite", "todolist":
		// Keep the checklist expanded so progress stays visible.
		return false
	case "writememory", "readmemory":
		// Short store/merge confirmations and memory lists read fine expanded.
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
			// The runtime label lives at the tail of the transcript now, so each
			// frame has to repaint the viewport to animate.
			m.repaintViewport()
			return m, tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return spinnerTickMsg(t) })
		}
		return m, nil
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			m.viewport.ScrollUp(3)
		} else if msg.Button == tea.MouseWheelDown {
			m.viewport.ScrollDown(3)
		}
		return m, nil
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			layout := m.layout()
			if msg.Y >= layout.inputY && msg.Y < layout.inputY+layout.inputHeight+1 {
				m.input.Focus()
				return m, nil
			}
			m.input.Blur()
			m.autocomplete = nil
			m.selectAllMode = false
		}
		return m, nil
	case tea.PasteMsg:
		m.lastPasteAt = time.Now()
		m.suppressNextPasteEnter = true
		if shouldFoldPastedText(msg.Content) {
			m.insertFoldedPaste(msg.Content)
			m.updateAutocomplete()
			return m, nil
		}
		// textarea handles a PasteMsg line-by-line in v2. Insert the complete
		// payload ourselves so newlines remain part of one paste operation.
		m.input.InsertString(strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n"))
		m.resize()
		m.prunePastedBlocks()
		m.updateAutocomplete()
		return m, nil
	case tea.KeyPressMsg:
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
			return m.handleDialogKey(msg)
		}
		if m.workflowEditor != nil {
			return m.handleWorkflowEditorKey(msg)
		}
		if m.autocomplete != nil {
			return m.handleAutocompleteKey(msg)
		}
		if m.pending != nil {
			return m.handlePermissionKey(msg.String())
		}
		if !m.input.Focused() && msg.Key().Text != "" {
			m.input.Focus()
		}
		if msg.Key().Code != tea.KeyEnter {
			m.suppressNextPasteEnter = false
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
			if !msg.Key().Mod.Contains(tea.ModAlt) {
				if m.suppressNextPasteEnter && time.Since(m.lastPasteAt) <= pasteEnterGuardWindow {
					m.suppressNextPasteEnter = false
					return m, nil
				}
				m.suppressNextPasteEnter = false
				prompt := strings.TrimSpace(m.input.Value())
				if prompt == "" {
					return m, nil
				}
				displayContent := pastedBlocksDisplay(m.pastedBlocks)
				prompt = m.expandPastedBlocks(prompt)
				m.pastedBlocks = nil
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
		// Switching/reloading a session resets cumulative request totals.
		m.resetSessionTokenTotals()
		m.refreshViewport()
		return m, nil
	case TokenUsageMsg:
		m.applyTokenUsage(msg)
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
		customInput.SetWidth(max(20, m.width-12))
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
	case AgentProgressMsg:
		m.applyAgentProgress(msg)
		m.resize()
		m.refreshViewport()
		return m, nil
	}

	if m.pendingConf != nil || m.pendingAsk != nil || m.pending != nil || (m.dialog != nil && m.dialog.Active != DialogNone) || m.workflowEditor != nil || m.autocomplete != nil || m.selectAllMode {
		return m, nil
	}
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resize()
	m.prunePastedBlocks()
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

func parseKeyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "tab":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	case "delete":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	default:
		return tea.KeyPressMsg(tea.Key{Text: key})
	}
}

func pastedLineCount(text string) int {
	if text == "" {
		return 0
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Count(text, "\n") + 1
}

func shouldFoldPastedText(text string) bool {
	return len([]rune(text)) >= foldedPasteMinChars || pastedLineCount(text) >= foldedPasteMinLines
}

func (m *Model) takeNextPasteID() int {
	m.nextPasteID++
	return m.nextPasteID
}

func foldedPasteLabel(id, lines int) string {
	return fmt.Sprintf("[Pasted text #%d · %d lines]", id, lines)
}

func renderFoldedPasteBlock(block pastedBlock) string {
	return fmt.Sprintf("%s\n\n--- Begin %s ---\n%s\n--- End %s ---", block.label, block.label, block.text, block.label)
}

func (m *Model) insertFoldedPaste(text string) {
	label := foldedPasteLabel(m.takeNextPasteID(), pastedLineCount(text))
	m.pastedBlocks = append(m.pastedBlocks, pastedBlock{label: label, text: text})
	if current := m.input.Value(); current != "" && !strings.HasSuffix(current, " ") {
		m.input.InsertString(" ")
	}
	m.input.InsertString(label + " ")
}

func (m *Model) expandPastedBlocks(displayed string) string {
	for _, block := range m.pastedBlocks {
		if strings.Contains(displayed, block.label) {
			displayed = strings.ReplaceAll(displayed, block.label, renderFoldedPasteBlock(block))
		}
	}
	return displayed
}

func (m *Model) prunePastedBlocks() {
	displayed := m.input.Value()
	kept := m.pastedBlocks[:0]
	for _, block := range m.pastedBlocks {
		if strings.Contains(displayed, block.label) {
			kept = append(kept, block)
		}
	}
	m.pastedBlocks = kept
}

func pastedBlocksDisplay(blocks []pastedBlock) string {
	lines := 0
	for _, block := range blocks {
		lines += pastedLineCount(block.text)
	}
	return pastedInputDisplay(lines)
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
	if len(m.history) > maxHistoryEntries {
		copy(m.history, m.history[len(m.history)-maxHistoryEntries:])
		m.history = m.history[:maxHistoryEntries]
	}
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

func (m *Model) SetModeSwitchFn(modeNames []string, currentMode string, fn func(mode string)) {
	m.modeNames = modeNames
	m.permissionMode = currentMode
	if m.permissionMode == "" {
		m.permissionMode = modeNames[0]
	}
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

func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.dialog == nil {
		return m, nil
	}
	key := strings.ToLower(msg.String())
	if m.dialog.Custom {
		return m.handleCustomDialogKey(msg, key)
	}
	switch key {
	case "esc", "ctrl+c":
		m.dialog = nil
	case "up", "k":
		if m.dialog.Selected > 0 {
			m.dialog.Selected--
		}
	case "down", "j":
		if m.dialog.Selected < len(m.dialog.Items)-1 {
			m.dialog.Selected++
		}
	case "enter":
		if m.dialog.Selected >= 0 && m.dialog.Selected < len(m.dialog.Items) {
			item := m.dialog.Items[m.dialog.Selected]
			if item.Custom {
				m.startCustomDialog()
				return m, nil
			}
			kind := m.dialog.Active
			m.dialog = nil
			result := SelectResult{Message: fmt.Sprintf("Selected: %s", item.Label)}
			if m.selectFunc != nil {
				result = m.selectFunc(kind, item.Value)
			}
			m.applySelectResult(result)
		}
	}
	m.status = "Ready"
	m.resize()
	m.refreshViewport()
	return m, nil
}

func (m *Model) startCustomDialog() {
	if m.dialog == nil {
		return
	}
	input := textinput.New()
	input.Placeholder = customDialogFieldPlaceholder(m.dialog.Active, 0)
	input.CharLimit = 1000
	input.SetWidth(max(20, m.width-12))
	input.Focus()
	m.dialog.Custom = true
	m.dialog.CustomStep = 0
	m.dialog.CustomValues = nil
	m.dialog.CustomInput = input
	m.status = "Input required"
	m.resize()
	m.refreshViewport()
}

func (m Model) handleCustomDialogKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	dialog := m.dialog
	if dialog == nil {
		return m, nil
	}
	switch key {
	case "esc":
		dialog.Custom = false
		dialog.CustomInput.Blur()
		m.status = "Ready"
	case "ctrl+c":
		m.dialog = nil
		m.status = "Ready"
	case "enter":
		value := strings.TrimSpace(dialog.CustomInput.Value())
		// API protocol is optional: empty means anthropic (default).
		if value == "" {
			if dialog.Active == DialogProvider && dialog.CustomStep == 3 {
				value = "anthropic"
			} else {
				return m, nil
			}
		}
		dialog.CustomValues = append(dialog.CustomValues, value)
		dialog.CustomInput.Reset()
		dialog.CustomStep++
		if dialog.CustomStep < customDialogFieldCount(dialog.Active) {
			dialog.CustomInput.Placeholder = customDialogFieldPlaceholder(dialog.Active, dialog.CustomStep)
			m.refreshViewport()
			return m, nil
		}
		kind := dialog.Active
		values := append([]string(nil), dialog.CustomValues...)
		m.dialog = nil
		result := SelectResult{Message: "Custom selection saved."}
		if m.customSelectFunc != nil {
			result = m.customSelectFunc(kind, values)
		}
		m.applySelectResult(result)
		return m, nil
	default:
		var cmd tea.Cmd
		dialog.CustomInput, cmd = dialog.CustomInput.Update(msg)
		m.resize()
		m.refreshViewport()
		return m, cmd
	}
	m.resize()
	m.refreshViewport()
	return m, nil
}

func customDialogFieldCount(kind DialogKind) int {
	if kind == DialogProvider {
		return 4
	}
	return 1
}

func customDialogFieldPlaceholder(kind DialogKind, step int) string {
	if kind == DialogProvider {
		switch step {
		case 0:
			return "Provider name"
		case 1:
			return "API key"
		case 2:
			return "Base URL"
		default:
			return "API protocol: anthropic or openai (default anthropic)"
		}
	}
	return "Model ID"
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
		m.resize()
		return m, nil
	case "down", "j":
		if m.autocomplete.Selected < len(m.autocomplete.Items)-1 {
			m.autocomplete.Selected++
		}
		m.resize()
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
	item := m.autocomplete.Items[m.autocomplete.Selected]
	switch m.autocomplete.Kind {
	case AutocompleteFile:
		value := m.input.Value()
		start := m.autocomplete.AtStart
		if start < 0 || start > len(value) {
			start = 0
		}
		// Replace from @ to end of current token with @selected
		// Keep a trailing space only for files (not directories ending with /).
		replacement := "@" + item
		if !strings.HasSuffix(item, "/") {
			replacement += " "
		}
		prefix := value[:start]
		newValue := prefix + replacement
		m.input.Reset()
		setTextareaValue(&m.input, newValue)
		m.autocomplete = nil
		// If we completed a directory, immediately re-open file suggestions.
		m.updateAutocomplete()
	default:
		m.input.Reset()
		full := "/" + item + " "
		setTextareaValue(&m.input, full)
		m.autocomplete = nil
	}
}

func (m *Model) updateAutocomplete() tea.Cmd {
	value := m.input.Value()

	// Slash commands: whole input starts with / and has no spaces yet.
	if strings.HasPrefix(value, "/") && !strings.Contains(value, " ") {
		prefix := strings.TrimPrefix(value, "/")
		commands := []string{"help", "status", "clear", "model", "provider", "effort", "sessions", "compact", "checkpoints", "checkpoint-name", "rewind", "fix-session", "new-session", "skills", "mcp", "proxy", "goal", "workflows", "workflow", "workflow-edit", "web-ui"}
		if m.workflowNamesFn != nil {
			commands = append(commands, m.directWorkflowSlashCommands()...)
		}
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
			m.resize()
			return nil
		}
		m.autocomplete = &AutocompleteState{
			Items:    matches,
			Selected: 0,
			Prefix:   prefix,
			Kind:     AutocompleteSlash,
		}
		m.resize()
		return nil
	}

	// File path suggestions for @token at end of input.
	if filePrefix, atStart, ok := attach.CurrentAtToken(value); ok {
		matches := attach.SuggestFiles(m.cwd, filePrefix)
		// Drop exact complete match when typing a full file name with no trailing slash.
		if filePrefix != "" && !strings.HasSuffix(filePrefix, "/") {
			filtered := matches[:0]
			for _, match := range matches {
				if match == filePrefix {
					continue
				}
				filtered = append(filtered, match)
			}
			matches = filtered
		}
		if len(matches) == 0 {
			m.autocomplete = nil
			m.resize()
			return nil
		}
		selected := 0
		if m.autocomplete != nil && m.autocomplete.Kind == AutocompleteFile {
			// Preserve selection when the list still contains the previous item.
			prev := ""
			if m.autocomplete.Selected >= 0 && m.autocomplete.Selected < len(m.autocomplete.Items) {
				prev = m.autocomplete.Items[m.autocomplete.Selected]
			}
			for i, match := range matches {
				if match == prev {
					selected = i
					break
				}
			}
		}
		m.autocomplete = &AutocompleteState{
			Items:    matches,
			Selected: selected,
			Prefix:   filePrefix,
			Kind:     AutocompleteFile,
			AtStart:  atStart,
		}
		m.resize()
		return nil
	}

	m.autocomplete = nil
	m.resize()
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
	return name == "bash" || name == "shell" || name == "wait" || strings.HasSuffix(name, ".bash")
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
		ToolUseID: strings.TrimSpace(msg.ToolUseID),
		Content:   msg.Input,
		Collapsed: true,
		TimeStamp: time.Now(),
	})
	m.trimMessages()
	m.updateTodosFromToolInput(msg)
}

func (m *Model) finishToolActivity(msg ToolDoneMsg) {
	m.messages = append(m.messages, ChatMessage{
		Role:      "tool-done",
		ToolName:  msg.Name,
		ToolUseID: strings.TrimSpace(msg.ToolUseID),
		Content:   msg.Output,
		IsError:   msg.IsError,
		Collapsed: defaultToolCollapsed(msg.Name),
		TimeStamp: time.Now(),
	})
	m.trimMessages()
}

// trimMessages keeps the displayed transcript within maxTUIMessages.
// It always removes from the front, so the newest content stays visible.
func (m *Model) trimMessages() {
	if len(m.messages) > maxTUIMessages {
		removed := len(m.messages) - maxTUIMessages
		m.messages = append(m.messages[:0], m.messages[removed:]...)
		m.invalidateRenderCache()
	}
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

// WithTodosForTest replaces the on-screen todo strip. Tests only.
func (m Model) WithTodosForTest(todos []TodoViewItem) Model {
	m.todos = append([]TodoViewItem(nil), todos...)
	m.resize()
	return m
}

func (m *Model) updateAgentActivity(msg AgentStatusMsg) {
	now := time.Now()
	activity := AgentActivity{
		ID:              msg.ID,
		ParentID:        msg.ParentID,
		ParentToolUseID: msg.ParentToolUseID,
		TaskID:          msg.TaskID,
		Role:            msg.Role,
		State:           msg.State,
		Description:     msg.Description,
		ToolName:        msg.ToolName,
		Output:          msg.Output,
		IsError:         msg.IsError,
		UpdatedAt:       now,
	}
	for i := range m.agentActivities {
		if m.agentActivities[i].ID == msg.ID {
			prev := m.agentActivities[i]
			if activity.ParentToolUseID == "" {
				activity.ParentToolUseID = prev.ParentToolUseID
			}
			if activity.TaskID == "" {
				activity.TaskID = prev.TaskID
			}
			if activity.ToolName == "" {
				activity.ToolName = prev.ToolName
			}
			if activity.Description == "" {
				activity.Description = prev.Description
			}
			activity.ProcessLines = prev.ProcessLines
			m.agentActivities[i] = activity
			return
		}
	}
	m.agentActivities = append(m.agentActivities, activity)
	if len(m.agentActivities) > 12 {
		m.agentActivities = m.agentActivities[len(m.agentActivities)-12:]
	}
}

func (m *Model) applyAgentProgress(msg AgentProgressMsg) {
	state := msg.Kind
	switch strings.ToLower(strings.TrimSpace(msg.Kind)) {
	case "started":
		state = "running"
	case "tool_start":
		state = "running"
	case "tool_done":
		state = "running"
	case "completed":
		state = "completed"
	case "failed":
		state = "failed"
	case "cancelled", "canceled":
		state = "cancelled"
	case "retry":
		state = "running"
	}
	output := strings.TrimSpace(msg.Output)
	if msg.Kind == "tool_start" && msg.ToolName != "" {
		output = "running " + msg.ToolName
	}
	if msg.Kind == "tool_done" && msg.ToolName != "" {
		if msg.IsError {
			output = msg.ToolName + " failed"
		} else {
			output = msg.ToolName + " done"
		}
		if summary := oneLine(msg.Output); summary != "" {
			output += ": " + truncate(summary, 80)
		}
	}
	m.updateAgentActivity(AgentStatusMsg{
		ID:              msg.AgentID,
		ParentID:        msg.ParentID,
		ParentToolUseID: msg.ParentToolUseID,
		TaskID:          msg.TaskID,
		Role:            "task",
		State:           state,
		Description:     msg.Description,
		ToolName:        msg.ToolName,
		Output:          output,
		IsError:         msg.IsError,
	})
	if line := agentProgressLine(msg); line != "" {
		m.appendAgentProcessLine(msg.AgentID, line)
	}
}

func agentProgressLine(msg AgentProgressMsg) string {
	switch msg.Kind {
	case "started":
		return "started"
	case "retry":
		if out := strings.TrimSpace(msg.Output); out != "" {
			return out
		}
		return "retrying"
	case "tool_start":
		if msg.ToolName == "" {
			return ""
		}
		return "→ " + msg.ToolName
	case "tool_done":
		if msg.ToolName == "" {
			return ""
		}
		line := msg.ToolName + " done"
		if msg.IsError {
			line = msg.ToolName + " failed"
		}
		if summary := oneLine(msg.Output); summary != "" {
			line += ": " + truncate(summary, 120)
		}
		return line
	case "completed", "failed", "cancelled", "canceled":
		line := msg.Kind
		if summary := oneLine(msg.Output); summary != "" {
			line += ": " + truncate(summary, 160)
		}
		return line
	default:
		return ""
	}
}

func (m *Model) appendAgentProcessLine(agentID, line string) {
	line = strings.TrimSpace(line)
	agentID = strings.TrimSpace(agentID)
	if line == "" || agentID == "" {
		return
	}
	for i := range m.agentActivities {
		if m.agentActivities[i].ID != agentID {
			continue
		}
		lines := append(append([]string{}, m.agentActivities[i].ProcessLines...), line)
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		m.agentActivities[i].ProcessLines = lines
		return
	}
}

func summarizeAgentProcess(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	keep := lines
	if len(keep) > 4 {
		keep = append([]string{keep[0], "…"}, keep[len(keep)-2:]...)
	}
	return strings.Join(keep, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("initializing...")
	}
	parts := []string{m.renderViewportWithScrollbar()}
	if dialog := m.renderActiveDialog(); dialog != "" {
		parts = append(parts, dialog)
	} else if m.autocomplete != nil {
		if autocomplete := m.renderAutocomplete(); autocomplete != "" {
			parts = append(parts, autocomplete)
		}
	}
	parts = append(parts, m.renderSessionBar(), m.renderInput(), m.renderUsageBar())
	if m.autocomplete == nil {
		if panel := m.renderActivityPanel(); panel != "" {
			parts = append(parts, panel)
		}
	}
	content := lipgloss.NewStyle().Width(m.width).Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	return view
}

func (m Model) renderViewportWithScrollbar() string {
	view := m.viewport.View()
	bar := m.renderScrollbar()
	return lipgloss.JoinHorizontal(lipgloss.Top, view, bar)
}

func (m Model) renderScrollbar() string {
	total := m.viewport.TotalLineCount()
	visible := m.viewport.VisibleLineCount()
	height := m.viewport.Height()
	if height <= 0 {
		return ""
	}
	// Nothing to scroll: drawing a full-height track puts a stray vertical line
	// down the right edge of an otherwise empty screen.
	if total <= visible {
		return ""
	}
	trackStyle := lipgloss.NewStyle().Foreground(m.theme.BorderSubtle)
	thumbStyle := lipgloss.NewStyle().Foreground(m.theme.Border)
	track := " " + glyphs.ScrollTrack
	thumb := " " + glyphs.ScrollThumb
	thumbHeight := max(1, visible*height/total)
	maxThumbPos := total - visible
	thumbPos := 0
	if maxThumbPos > 0 {
		thumbPos = m.viewport.YOffset() * (height - thumbHeight) / maxThumbPos
	}
	thumbPos = clamp(thumbPos, 0, height-thumbHeight)
	var b strings.Builder
	for i := 0; i < height; i++ {
		if i >= thumbPos && i < thumbPos+thumbHeight {
			b.WriteString(thumbStyle.Render(thumb))
		} else {
			b.WriteString(trackStyle.Render(track))
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
	case m.workflowEditor != nil:
		return m.renderWorkflowEditor()
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
	borderColor := t.Border
	if m.input.Focused() {
		borderColor = t.Claude
	}

	// Rounded to match message cards and dialogs. The usage meters used to live
	// inside this box and crowded the typed text; they now own the row below.
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		BorderTop(true).BorderBottom(true).BorderLeft(true).BorderRight(true).
		Padding(0, 1).
		Width(max(1, m.width-4))

	return inputStyle.Render(inputView)
}

// renderSessionBar carries only slow-moving context: model, permission mode,
// theme and working directory. The live Thinking/Ready label used to share this
// row, but it now trails the conversation itself (see renderRuntimeLine).
func (m Model) renderSessionBar() string {
	t := m.theme
	sep := t.Muted.Render(" " + glyphs.Separator + " ")

	modelName := strings.TrimSpace(m.currentModelName())
	if modelName == "" {
		modelName = "solcode"
	}
	left := " " + t.ClaudeStyle.Render(modelName)

	rightParts := []string{}
	if m.permissionMode != "" && m.permissionMode != "auto" {
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(t.Warning).Render(m.permissionMode))
	}
	if m.theme.Name != "" {
		rightParts = append(rightParts, t.Muted.Render(m.theme.Name))
	}
	if m.cwd != "" {
		rightParts = append(rightParts, t.Muted.Render(truncateWidth(m.cwd, 40)))
	}
	right := ""
	if len(rightParts) > 0 {
		right = strings.Join(rightParts, sep) + " "
	}

	gap := strings.Repeat(" ", max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right)-2))
	return t.Status.Width(m.width).Render(left + gap + right)
}

// renderRuntimeLine trails the transcript with the live activity label, so the
// one thing that actually changes sits right under the newest message instead of
// being parked in the bottom chrome.
func (m Model) renderRuntimeLine() string {
	t := m.theme
	label := strings.TrimSpace(m.status)

	if m.spinnerActive {
		line := renderSpinnerLabel(t, m.spinnerFrame, label, m.loadingStart)
		if m.activeToolName != "" {
			line += t.Muted.Render(" "+glyphs.Separator+" ") + t.Tool.Render(m.activeToolName)
		}
		line += t.Muted.Render(" " + glyphs.Separator + " ctrl+c interrupt")
		return line
	}

	// Idle "Ready" used to live in its own chrome row. Trailing it into the
	// transcript costs a permanent viewport line and scrolls short command
	// output (e.g. /help) off the top of a 20-row terminal — skip it.
	if label == "" || label == "Ready" {
		return ""
	}
	return t.Muted.Render(glyphs.Idle + " " + label)
}

// renderUsageBar puts the context/cache meters on their own row under the
// prompt. Inside the input box they fought the typed text for the same line.
func (m Model) renderUsageBar() string {
	status := m.renderUsageStatus()
	if status == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Width(max(1, m.width)).
		Render(" " + status)
}

func (m Model) renderUsageStatus() string {
	t := m.theme
	usage := m.tokenUsage
	used := m.displayContextTokens()
	limit := m.currentContextLimit()
	sep := t.Muted.Render(" " + glyphs.Separator + " ")

	// Always-visible footer. Meters are tinted by fill ratio so a nearly full
	// context / cache window reads as a warning without extra wording.
	meter := lipgloss.NewStyle().
		Foreground(t.ProgressColor(used, limit)).
		Render(renderContextProgressBar(used, limit, 10))
	parts := []string{fmt.Sprintf("%s %s %s/%s",
		meter,
		t.Muted.Render("ctx"),
		compactTokens(used),
		renderContextLimit(limit),
	)}

	inputSide := usageInputSideTotal(usage.InputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	cacheRead := max64(0, usage.CacheReadInputTokens)
	cacheWrite := max64(0, usage.CacheCreationInputTokens)
	cacheUsed := cacheRead + cacheWrite
	cacheMeter := lipgloss.NewStyle().
		Foreground(t.ProgressColor(cacheUsed, inputSide)).
		Render(renderContextProgressBar(cacheUsed, inputSide, 10))
	parts = append(parts, fmt.Sprintf("%s %s %s/%s",
		cacheMeter,
		t.Muted.Render("cache"),
		compactTokens(cacheUsed),
		compactTokens(inputSide),
	))
	parts = append(parts, fmt.Sprintf("%s %s",
		t.Muted.Render("out"),
		compactTokens(max64(0, usage.OutputTokens)),
	))
	return strings.Join(parts, sep)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// applyTokenUsage updates the latest context estimate and session token counters.
// SessionTotals messages replace absolute counters (from persisted session);
// otherwise values are treated as per-request deltas.
func (m *Model) applyTokenUsage(msg TokenUsageMsg) {
	if msg.EstimatedContextTokens > 0 {
		m.tokenUsage.EstimatedContextTokens = msg.EstimatedContextTokens
	}
	if msg.MaxContextTokens > 0 {
		m.tokenUsage.MaxContextTokens = msg.MaxContextTokens
	}
	if msg.SessionTotals {
		m.tokenUsage.InputTokens = msg.InputTokens
		m.tokenUsage.OutputTokens = msg.OutputTokens
		m.tokenUsage.CacheCreationInputTokens = msg.CacheCreationInputTokens
		m.tokenUsage.CacheReadInputTokens = msg.CacheReadInputTokens
		return
	}
	m.tokenUsage.InputTokens += msg.InputTokens
	m.tokenUsage.OutputTokens += msg.OutputTokens
	m.tokenUsage.CacheCreationInputTokens += msg.CacheCreationInputTokens
	m.tokenUsage.CacheReadInputTokens += msg.CacheReadInputTokens
}

// resetSessionTokenTotals clears cumulative request counters (cache/in/out)
// while preserving the last known context limit.
func (m *Model) resetSessionTokenTotals() {
	limit := m.tokenUsage.MaxContextTokens
	m.tokenUsage.InputTokens = 0
	m.tokenUsage.OutputTokens = 0
	m.tokenUsage.CacheCreationInputTokens = 0
	m.tokenUsage.CacheReadInputTokens = 0
	m.tokenUsage.EstimatedContextTokens = 0
	m.tokenUsage.MaxContextTokens = limit
}

// usageInputSideTotal is uncached input + cache read + cache write (session totals).
// Used as the denominator for separate cache-read / cache-write progress bars.
func usageInputSideTotal(inputTokens, cacheRead, cacheWrite int64) int64 {
	total := int64(0)
	if inputTokens > 0 {
		total += inputTokens
	}
	if cacheRead > 0 {
		total += cacheRead
	}
	if cacheWrite > 0 {
		total += cacheWrite
	}
	return total
}

// tokenSharePercent returns part/total as a percent string (always, including "0%").
func tokenSharePercent(part, total int64) string {
	if part < 0 {
		part = 0
	}
	if total <= 0 {
		return "0%"
	}
	percent := part * 100 / total
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%d%%", percent)
}

// cacheSharePercent reports combined cache (read+write) share of input-side tokens.
// Kept for tests / callers that want a single combined percentage.
func cacheSharePercent(inputTokens, cacheRead, cacheWrite int64) string {
	return tokenSharePercent(cacheRead+cacheWrite, usageInputSideTotal(inputTokens, cacheRead, cacheWrite))
}

// renderContextProgressBar draws a fixed-width filled bar for used/limit.
// Uses block characters that render well in common terminals.
func renderContextProgressBar(used, limit int64, width int) string {
	if width < 4 {
		width = 4
	}
	if limit <= 0 {
		return strings.Repeat(glyphs.BarEmpty, width)
	}
	// Floor division for share bars; round-up only when used > 0 so tiny non-zero
	// values still show one block (same as context occupancy).
	filled := int(used * int64(width) / limit)
	if used > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat(glyphs.BarFilled, filled) + strings.Repeat(glyphs.BarEmpty, width-filled)
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
		// Include @attachment expansion (esp. image vision tokens) in the live estimate.
		extra := int64(tokenest.Text(input))
		if strings.Contains(input, "@") {
			expanded := attach.Expand(input, m.cwd)
			extra = int64(expanded.EstimatedTokens())
		}
		return m.tokenUsage.EstimatedContextTokens + extra
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
	textTokens := int64(tokenest.Text(b.String()))
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		return base + textTokens
	}
	// Prefer full @attachment expansion so image vision tokens are counted.
	if strings.Contains(input, "@") {
		expanded := attach.Expand(input, m.cwd)
		return base + textTokens + int64(expanded.EstimatedTokens())
	}
	return base + textTokens + int64(tokenest.Text("user: "+input))
}

func (m Model) renderAutocomplete() string {
	if m.autocomplete == nil || len(m.autocomplete.Items) == 0 {
		return ""
	}

	// Keep the final completion menu within the exact space reserved by layout.
	// Unlike a MaxHeight style applied after rendering, this accounts for the
	// header before choosing list rows, so the menu can never push out the
	// composer on a short terminal.
	maxLines := m.maxAutocompleteListLines()
	if maxLines < 1 {
		return ""
	}

	t := m.theme
	label := "Commands:"
	prefix := "/"
	if m.autocomplete.Kind == AutocompleteFile {
		label = "Files:"
		prefix = "@"
	}
	items := m.autocomplete.Items
	start, end := visibleRange(m.autocomplete.Selected, len(items), maxLines)

	var itemLines []string
	for i := start; i < end; i++ {
		display := prefix + items[i]
		if i == m.autocomplete.Selected {
			itemLines = append(itemLines, "  "+t.ClaudeStyle.Render("❯ "+display))
		} else {
			itemLines = append(itemLines, "    "+t.Dim.Render(display))
		}
	}
	listWidth := max(20, min(m.width-6, 56))
	list := m.renderScrollableList(itemLines, len(items), start, maxLines, listWidth)
	header := "  " + t.Dim.Render(label)
	if len(items) > maxLines {
		header += t.Dim.Render(fmt.Sprintf("  %d/%d", m.autocomplete.Selected+1, len(items)))
	}
	return m.fitOverlay(header + "\n" + list)
}

func (m Model) renderDialog() string {
	t := m.theme
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render(m.dialog.Title)
	if m.dialog.Custom {
		return m.renderCustomDialog(title, dialogWidth)
	}
	items := m.dialog.Items
	maxLines := m.maxOverlayListLines()
	start, end := visibleRange(m.dialog.Selected, len(items), maxLines)

	var itemLines []string
	for i := start; i < end; i++ {
		item := items[i]
		line := "  " + item.Label
		if i == m.dialog.Selected {
			line = t.ClaudeStyle.Render("❯ ") + t.ClaudeStyle.Render(item.Label)
		}
		if item.Current {
			line += t.Dim.Render(" (current)")
		}
		if item.Subtitle != "" {
			line += "  " + t.Dim.Render(item.Subtitle)
		}
		itemLines = append(itemLines, line)
	}
	listWidth := max(16, dialogWidth-6)
	list := m.renderScrollableList(itemLines, len(items), start, maxLines, listWidth)
	hint := t.PermHint.Render(fmt.Sprintf("[↑/↓] Navigate  [Enter] Select  [Esc] Cancel  %d/%d", m.dialog.Selected+1, max(1, len(items))))
	body := strings.Join([]string{title, list, hint}, "\n")
	return m.fitOverlay(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body)))
}

func (m Model) renderCustomDialog(title string, dialogWidth int) string {
	dialog := m.dialog
	field := customDialogFieldLabel(dialog.Active, dialog.CustomStep)
	hint := m.theme.PermHint.Render("[Enter] Continue  [Esc] Back to choices  [Ctrl+C] Cancel")
	body := strings.Join([]string{title, "", field, "", "  " + dialog.CustomInput.View(), "", hint}, "\n")
	return m.fitOverlay(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, m.theme.DialogBorder.Width(dialogWidth).Render(body)))
}

func customDialogFieldLabel(kind DialogKind, step int) string {
	if kind == DialogProvider {
		switch step {
		case 0:
			return "Provider name"
		case 1:
			return "API key"
		case 2:
			return "Base URL"
		default:
			return "API protocol (anthropic or openai)"
		}
	}
	return "Model ID"
}

func (m *Model) ShowDialog(kind DialogKind) {
	if m.itemsFunc == nil {
		return
	}
	items := m.itemsFunc(kind)
	if kind == DialogModel || kind == DialogProvider {
		items = append(items, DialogItem{Label: "Custom…", Subtitle: "Add and save a custom entry", Custom: true})
	}
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
	m.resize()
	m.refreshViewport()
}

func (m Model) renderPermissionDialog() string {
	t := m.theme
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render(ErrorMark + "  Permission Required")
	tool := lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(m.pending.toolName)
	desc := t.Muted.Render(truncate(strings.TrimSpace(m.pending.description), 600))
	hint := lipgloss.NewStyle().Foreground(t.Success).Render("[y] Allow") +
		t.PermHint.Render("   ") +
		lipgloss.NewStyle().Foreground(t.Error).Render("[n] Deny")
	body := strings.Join([]string{title, "", t.Muted.Render("Tool: ") + tool, desc, "", hint}, "\n")
	return m.fitOverlay(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body)))
}

func (m Model) renderConfirmDialog() string {
	t := m.theme
	dialogWidth := min(60, m.width-4)
	title := t.PermTitle.Render("Confirm")
	question := truncate(strings.TrimSpace(m.pendingConf.question), 600)
	hint := t.PermHint.Render("[y] Yes   [n] No")
	body := strings.Join([]string{title, "", question, "", hint}, "\n")
	return m.fitOverlay(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body)))
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
	// Options + trailing "Custom answer" row participate in the scroll window.
	optionCount := len(q.Options)
	totalRows := optionCount + 1 // custom answer
	maxLines := m.maxOverlayListLines()
	// Reserve 1 extra visual line when custom row shows its input field.
	listBudget := maxLines
	start, end := visibleRange(ask.selected, totalRows, listBudget)
	// Drop previews when the list is long so each option stays one line.
	showPreview := optionCount <= listBudget

	var itemLines []string
	for i := start; i < end; i++ {
		if i == optionCount {
			customMarker := "  "
			if ask.selected == optionCount {
				customMarker = t.ClaudeStyle.Render("❯ ")
			}
			// Custom field always shows input when in the window.
			itemLines = append(itemLines, customMarker+"Custom answer")
			itemLines = append(itemLines, "    "+ask.customInput.View())
			continue
		}
		opt := q.Options[i]
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
		itemLines = append(itemLines, line)
		if showPreview && strings.TrimSpace(opt.Preview) != "" {
			itemLines = append(itemLines, "    "+t.Dim.Render(truncate(oneLine(opt.Preview), max(20, m.width-8))))
		}
	}
	// Keep custom input visible when selected but window ended before custom row.
	if ask.selected == optionCount && end <= optionCount {
		customMarker := t.ClaudeStyle.Render("❯ ")
		itemLines = append(itemLines, customMarker+"Custom answer", "    "+ask.customInput.View())
	}

	dialogWidth := min(60, m.width-4)
	listWidth := max(16, dialogWidth-6)
	// Scrollbar tracks logical rows (options+custom), not wrapped preview lines.
	list := m.renderScrollableList(itemLines, totalRows, start, listBudget, listWidth)

	hint := fmt.Sprintf("[↑/↓] Navigate  [Enter] Select or enter custom answer  [Esc] Cancel  %d/%d", ask.selected+1, totalRows)
	if q.MultiSelect {
		hint = fmt.Sprintf("[↑/↓] Navigate  [Space] Toggle  [Enter] Submit  [Esc] Cancel  %d/%d", ask.selected+1, totalRows)
	}
	if ask.editingCustom {
		hint = "[Enter] Submit custom answer  [Esc] Back to choices  [Ctrl+C] Cancel"
	}
	body := strings.Join([]string{
		title,
		truncate(strings.TrimSpace(q.Question), 600),
		list,
		t.PermHint.Render(hint),
	}, "\n")
	return m.fitOverlay(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, t.DialogBorder.Width(dialogWidth).Render(body)))
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
	done := 0
	for _, todo := range m.todos {
		if strings.EqualFold(strings.TrimSpace(todo.Status), "completed") {
			done++
		}
	}
	bar := t.PanelBar.Render(glyphs.PanelBar)
	title := t.PanelTitle.Render("Todos") + t.Muted.Render(fmt.Sprintf(" %d/%d", done, len(m.todos)))
	lines := []string{" " + bar + " " + title}
	for _, todo := range m.todos[:limit] {
		marker := t.Muted.Render(glyphs.TodoPending)
		text := truncateWidth(oneLine(todo.Content), max(20, m.width-10))
		switch strings.ToLower(todo.Status) {
		case "in_progress":
			marker = lipgloss.NewStyle().Foreground(t.Claude).Render(glyphs.TodoActive)
			text = lipgloss.NewStyle().Foreground(t.Text).Render(text)
		case "completed":
			marker = lipgloss.NewStyle().Foreground(t.Success).Render(glyphs.TodoDone)
			text = t.Muted.Strikethrough(true).Render(text)
		default:
			text = t.Muted.Render(text)
		}
		lines = append(lines, " "+bar+" "+marker+" "+text)
	}
	if len(m.todos) > limit {
		lines = append(lines, " "+bar+" "+t.Muted.Render(fmt.Sprintf("+%d more", len(m.todos)-limit)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderAgentPanel() string {
	activities := m.visibleAgentActivities()
	if len(activities) == 0 {
		return ""
	}
	t := m.theme
	sections := make([]string, 0, len(activities))
	for _, activity := range activities {
		sections = append(sections, m.renderOneAgentPanel(activity, t))
	}
	// Separate each subagent into its own panel block.
	return strings.Join(sections, "\n")
}

func (m Model) visibleAgentActivities() []AgentActivity {
	if len(m.agentActivities) == 0 {
		return nil
	}
	limit := min(4, len(m.agentActivities))
	start := len(m.agentActivities) - limit
	return m.agentActivities[start:]
}

func (m Model) renderOneAgentPanel(activity AgentActivity, t Theme) string {
	bar := t.PanelBar.Render(glyphs.PanelBar)
	label := firstNonEmpty(activity.Description, activity.TaskID, activity.ID)
	state := strings.ToLower(strings.TrimSpace(activity.State))
	marker := t.Muted.Render("•")
	stateLabel := state
	switch state {
	case "running", "started":
		marker = lipgloss.NewStyle().Foreground(t.Claude).Render("●")
		stateLabel = "running"
	case "completed":
		marker = lipgloss.NewStyle().Foreground(t.Success).Render("✓")
	case "failed":
		marker = lipgloss.NewStyle().Foreground(t.Error).Render("✗")
	case "cancelled", "canceled":
		marker = t.Muted.Render("○")
		stateLabel = "cancelled"
	case "":
		stateLabel = "idle"
	}
	titleWidth := max(12, m.width-18)
	title := truncateWidth(oneLine(label), titleWidth)
	lines := []string{" " + bar + " " + t.PanelTitle.Render(title) + " " + marker + " " + t.Muted.Render(stateLabel)}

	process := activity.ProcessLines
	if len(process) == 0 {
		if activity.ToolName != "" && (state == "running" || state == "started") {
			process = []string{"→ " + activity.ToolName}
		} else if detail := strings.TrimSpace(activity.Output); detail != "" {
			process = []string{oneLine(detail)}
		}
	} else if state != "running" && state != "started" && len(process) > 4 {
		process = strings.Split(summarizeAgentProcess(process), "\n")
	}
	for _, line := range process {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, " "+bar+" "+t.Muted.Render(truncate(oneLine(line), max(20, m.width-8))))
	}
	return strings.Join(lines, "\n")
}

func (m Model) activityPanelHeight() int {
	height := 0
	if len(m.todos) > 0 {
		height += min(4, len(m.todos)+1)
	}
	for _, activity := range m.visibleAgentActivities() {
		height++ // title
		processCount := len(activity.ProcessLines)
		if processCount == 0 {
			state := strings.ToLower(strings.TrimSpace(activity.State))
			if activity.ToolName != "" && (state == "running" || state == "started") {
				processCount = 1
			} else if strings.TrimSpace(activity.Output) != "" {
				processCount = 1
			}
		} else {
			state := strings.ToLower(strings.TrimSpace(activity.State))
			if state != "running" && state != "started" && processCount > 4 {
				processCount = 4
			}
		}
		height += processCount
	}
	return min(16, height)
}

func (m *Model) resize() {
	layout := m.layout()
	m.viewport.SetWidth(max(1, m.width-scrollbarWidth))
	m.viewport.SetHeight(layout.viewportHeight)
	m.input.SetWidth(layout.inputWidth)
	m.input.MinHeight = layout.inputHeight
	m.input.MaxHeight = layout.inputHeight
	m.input.SetHeight(layout.inputHeight)
	if m.pendingAsk != nil {
		m.pendingAsk.customInput.SetWidth(max(20, m.width-12))
	}
	if m.dialog != nil && m.dialog.Custom {
		m.dialog.CustomInput.SetWidth(max(20, m.width-12))
	}
}

func (m Model) layout() tuiLayout {
	// Bottom chrome, top→bottom under the viewport:
	//   session bar (1) + input box (2 text + 2 border = 4) + usage (1) + activity
	// statusHeight covers session + usage. Earlier math treated the input as 4
	// total rows while it actually painted 6, which oversized the viewport.
	inputHeight := 4
	statusHeight := 2 // session row + usage row
	usageHeight := 1
	dialogHeight := m.activeDialogHeight()
	activityHeight := m.activityPanelHeight()
	if m.autocomplete != nil {
		// Completion UI temporarily owns the bottom chrome.
		activityHeight = 0
	}
	return tuiLayout{
		viewportHeight: max(1, m.height-inputHeight-statusHeight-dialogHeight-activityHeight),
		inputWidth:     max(1, m.width-6),
		inputHeight:    2,
		statusHeight:   statusHeight,
		dialogHeight:   dialogHeight,
		permHeight:     dialogHeight,
		activityHeight: activityHeight,
		// input sits above the usage row and activity panel.
		inputY:    max(0, m.height-activityHeight-usageHeight-inputHeight),
		dialogY:   max(0, m.height-inputHeight-statusHeight-dialogHeight-activityHeight),
		permY:     max(0, m.height-activityHeight),
		activityY: max(0, m.height-activityHeight),
	}
}

func (m Model) activeDialogHeight() int {
	content := ""
	switch {
	case m.pendingConf != nil:
		content = m.renderConfirmDialog()
	case m.pendingAsk != nil:
		content = m.renderAskUserDialog()
	case m.dialog != nil && m.dialog.Active != DialogNone:
		content = m.renderDialog()
	case m.workflowEditor != nil:
		content = m.renderWorkflowEditor()
	case m.pending != nil:
		content = m.renderPermissionDialog()
	case m.autocomplete != nil:
		content = m.renderAutocomplete()
	default:
		return 0
	}
	if content == "" {
		return 0
	}
	h := lipgloss.Height(content)
	maxH := m.maxOverlayTotalHeight()
	if h > maxH {
		return maxH
	}
	return h
}

// maxOverlayListLines is how many option rows an overlay may paint before
// scrolling. Sized so title + list + hint (+ borders) fit in the overlay budget
// and are not clipped by status/input chrome below.
func (m Model) maxAutocompleteListLines() int {
	if m.height <= 0 {
		return 8
	}
	// Keep a usable completion list; fitOverlay still clamps to the overlay budget.
	budget := m.maxOverlayTotalHeight() - 1
	if budget < 1 {
		budget = 1
	}
	return min(8, budget)
}

func (m Model) maxOverlayListLines() int {
	if m.height <= 0 {
		return 8
	}
	totalH := m.maxOverlayTotalHeight()
	// border(2) + title(1) + hint(1) [+ question(1) for AskUser]
	chrome := 4
	if m.pendingAsk != nil {
		chrome = 5
	}
	budget := totalH - chrome
	if budget < 2 {
		budget = 2
	}
	// Cap so a giant terminal still feels like a panel, not a full-screen dump.
	capByHeight := max(3, (m.height*6)/10)
	if budget > capByHeight {
		budget = capByHeight
	}
	return budget
}

// maxOverlayTotalHeight is the absolute height budget for any overlay block.
// Leaves input + status (+ activity) and one chat line so the popup is not
// covered by the bottom chrome.
func (m Model) maxOverlayTotalHeight() int {
	if m.height <= 0 {
		return 12
	}
	const inputHeight = 4
	const statusHeight = 2
	const minimumViewportHeight = 1
	activity := m.activityPanelHeight()
	// Slash/@ autocomplete is higher priority than the todo/agent strip. If both
	// compete for the bottom chrome on a short terminal, prefer keeping the
	// completion list visible.
	if m.autocomplete != nil {
		activity = 0
	}
	return max(0, m.height-inputHeight-statusHeight-activity-minimumViewportHeight)
}

// fitOverlay clamps overlay content so JoinVertical never exceeds the terminal.
func (m Model) fitOverlay(content string) string {
	if content == "" || m.height <= 0 {
		return content
	}
	maxH := m.maxOverlayTotalHeight()
	if maxH < 1 {
		return ""
	}
	if lipgloss.Height(content) <= maxH {
		return content
	}
	return lipgloss.NewStyle().MaxHeight(maxH).Width(max(1, m.width)).Render(content)
}

// renderScrollableList paints option lines with a right-edge scrollbar when the
// list is longer than the visible window. Title/hint stay outside this block so
// they are never covered by scrolling content.
//
// total = full item count, offset = first visible index, maxVisible = window
// capacity used for thumb math (typically maxOverlayListLines).
func (m Model) renderScrollableList(itemLines []string, total, offset, maxVisible, listWidth int) string {
	visible := len(itemLines)
	if visible < 1 {
		visible = 1
		itemLines = []string{""}
	}
	if maxVisible < 1 {
		maxVisible = visible
	}
	// Track height matches what we paint (no empty padding for short lists).
	trackHeight := visible
	needsBar := total > visible

	contentWidth := max(8, listWidth)
	if needsBar {
		contentWidth = max(8, listWidth-2)
	}
	padded := padListColumn(itemLines, contentWidth)
	if !needsBar {
		return padded
	}
	// Thumb math uses the logical window capacity so position stays stable.
	bar := m.renderListScrollbar(total, min(maxVisible, total), offset, trackHeight)
	return lipgloss.JoinHorizontal(lipgloss.Top, padded, bar)
}

// renderListScrollbar draws a vertical track matching the chat scrollbar style.
// total = full item count, visible = window size, offset = first visible index,
// height = painted track rows.
func (m Model) renderListScrollbar(total, visible, offset, height int) string {
	if height <= 0 {
		return ""
	}
	trackStyle := lipgloss.NewStyle().Foreground(m.theme.BorderSubtle)
	thumbStyle := lipgloss.NewStyle().Foreground(m.theme.Border)
	if total <= visible {
		var b strings.Builder
		for i := 0; i < height; i++ {
			b.WriteString(trackStyle.Render(" " + glyphs.ScrollTrack))
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
		thumbPos = offset * (height - thumbHeight) / maxThumbPos
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

func padListColumn(lines []string, width int) string {
	if width < 1 {
		width = 1
	}
	var b strings.Builder
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			line = trimToWidth(line, width)
			w = lipgloss.Width(line)
		}
		if w < width {
			line += strings.Repeat(" ", width-w)
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func trimToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	// Walk runes until display width fits, leave room for ellipsis when possible.
	ellipsis := "…"
	limit := width
	if width > 1 {
		limit = width - lipgloss.Width(ellipsis)
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > limit {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	if width > 1 {
		b.WriteString(ellipsis)
	}
	return b.String()
}

// visibleRange returns a half-open [start,end) window that keeps selected in
// view when total exceeds maxVisible.
func visibleRange(selected, total, maxVisible int) (start, end int) {
	if total <= 0 {
		return 0, 0
	}
	if maxVisible < 1 {
		maxVisible = 1
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	if total <= maxVisible {
		return 0, total
	}
	start = selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	if start > total-maxVisible {
		start = total - maxVisible
	}
	return start, start + maxVisible
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
	m.invalidateRenderCache()
}

func (m *Model) invalidateRenderCache() {
	m.renderedMsgVersion++
}

func (m *Model) refreshViewport() {
	m.invalidateRenderCache()
	m.viewport.SetContent(m.viewportContent())
	m.viewport.GotoBottom()
}

// repaintViewport re-renders the transcript without forcing the scroll position.
// Spinner ticks go through here so an animating runtime line cannot yank a user
// who scrolled up back down to the bottom.
func (m *Model) repaintViewport() {
	atBottom := m.viewport.AtBottom()
	offset := m.viewport.YOffset()
	// On spinner ticks the message list has not changed — reuse the cached
	// rendered body and only append the updated runtime line. This avoids
	// re-rendering the entire transcript (potentially MBs) every 120 ms.
	content := m.viewportContentCached()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
		return
	}
	m.viewport.SetYOffset(offset)
}

// viewportContentCached renders messages only when the list version or width
// has changed; on spinner ticks the cached body is reused.
func (m *Model) viewportContentCached() string {
	w := m.viewport.Width()
	if m.renderedContent == "" || m.renderedMsgVersion != m.renderedCachedVersion || m.renderedWidth != w {
		body := strings.TrimRight(renderMessages(m.messages, m.theme, m.showTimestamp, w), "\n")
		m.renderedContent = body
		m.renderedWidth = w
		m.renderedCachedVersion = m.renderedMsgVersion
	}
	body := m.renderedContent
	runtime := m.renderRuntimeLine()
	if runtime == "" {
		return body
	}
	if body == "" {
		return runtime
	}
	return body + "\n\n" + runtime
}

// viewportContent is the transcript plus the trailing runtime label.
func (m Model) viewportContent() string {
	body := strings.TrimRight(renderMessages(m.messages, m.theme, m.showTimestamp, m.viewport.Width()), "\n")
	runtime := m.renderRuntimeLine()
	if runtime == "" {
		return body
	}
	if body == "" {
		return runtime
	}
	return body + "\n\n" + runtime
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
