package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// How often what's on screen is asked for again, as k9s refreshes.
const refreshEvery = 2 * time.Second

type screen int

const (
	previewsScreen screen = iota
	describeScreen
	logsScreen
)

// Model is the whole UI: which screen, what's selected, and what Stagd said.
type Model struct {
	src Source
	ctx Context
	now func() time.Time

	width, height int

	// Screens opened, the last on top: esc goes back one.
	screens []screen

	previews []*apiv1.Preview
	loaded   bool
	cursor   int
	filter   string

	// The preview described or whose logs are followed.
	current *apiv1.Preview
	scroll  int

	logs      []*apiv1.LogLine
	logsSince *timestamppb.Timestamp
	logsSeen  map[string]bool
	following bool

	// The prompt at the top: ":" for a command, "/" for a filter.
	prompt rune
	input  string

	confirm *confirmation

	flash      string
	flashError bool
	flashUntil time.Time
}

// confirmation is the dialog before something that can't be undone.
type confirmation struct {
	text   string
	ok     bool
	action tea.Cmd
}

// New makes the UI for previews from src.
func New(src Source, c Context) Model {
	return Model{src: src, ctx: c, now: time.Now, screens: []screen{previewsScreen}, width: 120, height: 36}
}

type (
	previewsMsg struct {
		previews []*apiv1.Preview
		err      error
	}
	previewMsg struct {
		preview *apiv1.Preview
		err     error
	}
	logsMsg struct {
		id    string
		lines []*apiv1.LogLine
		err   error
	}
	doneMsg struct {
		text string
		err  error
	}
	tickMsg time.Time
)

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadPreviews(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) loadPreviews() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		ps, err := m.src.Previews(ctx)

		return previewsMsg{ps, err}
	}
}

func (m Model) loadPreview(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		p, err := m.src.Preview(ctx, id)

		return previewMsg{p, err}
	}
}

func (m Model) loadLogs(id string, since *timestamppb.Timestamp) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		lines, err := m.src.Logs(ctx, id, since)

		return logsMsg{id, lines, err}
	}
}

func (m Model) top() screen {
	return m.screens[len(m.screens)-1]
}

// shown is the previews the filter lets through.
func (m Model) shown() []*apiv1.Preview {
	if m.filter == "" {
		return m.previews
	}

	f := strings.ToLower(m.filter)
	var out []*apiv1.Preview

	for _, p := range m.previews {
		hay := strings.ToLower(strings.Join([]string{p.GetRepository().GetFullName(), what(p), title(p), statusLabel(p.GetStatus()), p.GetBranch()}, " "))

		if strings.Contains(hay, f) {
			out = append(out, p)
		}
	}

	return out
}

func (m Model) selected() *apiv1.Preview {
	ps := m.shown()

	if m.cursor < 0 || m.cursor >= len(ps) {
		return nil
	}

	return ps[m.cursor]
}

func (m *Model) say(text string, isErr bool) {
	m.flash = text
	m.flashError = isErr
	m.flashUntil = m.now().Add(5 * time.Second)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tick(), m.loadPreviews()}

		if m.current != nil && m.top() == describeScreen {
			cmds = append(cmds, m.loadPreview(m.current.GetId()))
		}

		if m.current != nil && m.top() == logsScreen {
			cmds = append(cmds, m.loadLogs(m.current.GetId(), m.logsSince))
		}

		return m, tea.Batch(cmds...)

	case previewsMsg:
		if msg.err != nil {
			m.say(msg.err.Error(), true)

			return m, nil
		}

		m.previews = msg.previews
		m.loaded = true
		m.cursor = min(m.cursor, max(len(m.shown())-1, 0))

		return m, nil

	case previewMsg:
		if msg.err == nil && m.current != nil && msg.preview.GetId() == m.current.GetId() {
			m.current = msg.preview
		}

		return m, nil

	case logsMsg:
		if m.current == nil || msg.id != m.current.GetId() {
			return m, nil
		}

		if msg.err != nil {
			m.say(msg.err.Error(), true)

			return m, nil
		}

		m.addLogs(msg.lines)

		return m, nil

	case doneMsg:
		if msg.err != nil {
			m.say(msg.err.Error(), true)
		} else {
			m.say(msg.text, false)
		}

		return m, m.loadPreviews()

	case tea.KeyPressMsg:
		return m.key(msg)
	}

	return m, nil
}

// addLogs keeps the lines not already had: those at the last time are asked for again. Scrolled
// back, the view stays on the lines it shows as new ones come in below.
func (m *Model) addLogs(lines []*apiv1.LogLine) {
	added := 0

	defer func() {
		if !m.following {
			m.scroll = min(m.scroll+added, m.logsMax())
		}
	}()

	for _, l := range lines {
		k := lineKey(l)

		if m.logsSeen[k] {
			continue
		}

		m.logs = append(m.logs, l)
		m.logsSeen[k] = true
		added++
	}

	if n := len(m.logs); n > 0 {
		m.logsSince = m.logs[n-1].GetTime()
	}

	// Enough to scroll back through; older lines go.
	if n := len(m.logs); n > 5000 {
		m.logs = m.logs[n-5000:]
	}
}

// logsPage is how many log lines fit: the box less its autoscroll line and the gap under it.
func (m Model) logsPage() int {
	return max(m.bodyHeight()-2, 1)
}

// logsMax is how far back the logs scroll: to their first line at the top, no further.
func (m Model) logsMax() int {
	return max(len(m.logs)-m.logsPage(), 0)
}

// describeMax is how far down a description scrolls: to its last line at the bottom.
func (m Model) describeMax() int {
	if m.current == nil {
		return 0
	}

	all := m
	all.scroll = 0
	_, lines := all.describe()

	return max(len(lines)-m.bodyHeight(), 0)
}

func lineKey(l *apiv1.LogLine) string {
	return fmt.Sprintf("%d.%d %s %s", l.GetTime().GetSeconds(), l.GetTime().GetNanos(), l.GetService(), l.GetText())
}

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()

	if k == "ctrl+c" {
		return m, tea.Quit
	}

	if m.confirm != nil {
		return m.confirmKey(k)
	}

	if m.prompt != 0 {
		return m.promptKey(msg)
	}

	switch k {
	case ":", "/":
		m.prompt = []rune(k)[0]
		m.input = ""

		if k == "/" {
			m.input = m.filter
		}

		return m, nil
	case "esc":
		if m.top() == previewsScreen && m.filter != "" {
			m.filter = ""

			return m, nil
		}

		return m.back(), nil
	case "q":
		if len(m.screens) > 1 {
			return m.back(), nil
		}

		return m, tea.Quit
	}

	switch m.top() {
	case describeScreen:
		return m.describeKey(k)
	case logsScreen:
		return m.logsKey(k)
	default:
		return m.previewsKey(k)
	}
}

func (m Model) back() Model {
	if len(m.screens) > 1 {
		m.screens = m.screens[:len(m.screens)-1]
		m.scroll = 0
	}

	if m.top() == previewsScreen {
		m.current = nil
	}

	return m
}

func (m Model) open(s screen, p *apiv1.Preview) (Model, tea.Cmd) {
	if p == nil {
		return m, nil
	}

	m.current = p
	m.scroll = 0

	// From one preview's screen to the other's, not on top of it.
	if m.top() != previewsScreen {
		m.screens = m.screens[:len(m.screens)-1]
	}

	m.screens = append(m.screens, s)

	if s == logsScreen {
		m.logs = nil
		m.logsSince = nil
		m.logsSeen = map[string]bool{}
		m.following = true

		return m, m.loadLogs(p.GetId(), nil)
	}

	return m, m.loadPreview(p.GetId())
}

func (m Model) previewsKey(k string) (tea.Model, tea.Cmd) {
	n := len(m.shown())

	switch k {
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(n-1, 0))
	case "pgup", "ctrl+b":
		m.cursor = max(m.cursor-m.bodyHeight(), 0)
	case "pgdown", "ctrl+f":
		m.cursor = min(m.cursor+m.bodyHeight(), max(n-1, 0))
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(n-1, 0)
	case "enter", "d":
		return m.open(describeScreen, m.selected())
	case "l":
		return m.open(logsScreen, m.selected())
	default:
		return m.actionKey(k, m.selected())
	}

	return m, nil
}

// actionKey is what can be done to a preview from any of its screens.
func (m Model) actionKey(k string, p *apiv1.Preview) (tea.Model, tea.Cmd) {
	if p == nil {
		return m, nil
	}

	switch k {
	case "o":
		if p.GetUrl() == "" {
			m.say("It has no URL yet.", true)

			return m, nil
		}

		m.say("Opening "+p.GetUrl(), false)

		return m, openURL(p.GetUrl())
	case "p":
		if p.GetChange().GetUrl() == "" {
			m.say("It has no pull request.", true)

			return m, nil
		}

		return m, openURL(p.GetChange().GetUrl())
	case "w":
		if p.GetStatus() != apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING {
			m.say("Only a sleeping preview wakes.", true)

			return m, nil
		}

		id := p.GetId()

		m.say("Waking "+what(p)+"…", false)

		return m, func() tea.Msg {
			err := m.src.Wake(context.Background(), id)

			return doneMsg{"Waking " + what(p) + ".", err}
		}
	case "ctrl+d":
		id := p.GetId()

		m.confirm = &confirmation{
			text: fmt.Sprintf("Destroy %s on %s?\nIts database goes with it.", what(p), p.GetRepository().GetFullName()),
			action: func() tea.Msg {
				err := m.src.Destroy(context.Background(), id)

				return doneMsg{"Destroying " + what(p) + ".", err}
			},
		}
	}

	return m, nil
}

func (m Model) describeKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.scroll = max(m.scroll-1, 0)
	case "down", "j":
		m.scroll = min(m.scroll+1, m.describeMax())
	case "pgdown", "ctrl+f":
		m.scroll = min(m.scroll+m.bodyHeight(), m.describeMax())
	case "pgup", "ctrl+b":
		m.scroll = max(m.scroll-m.bodyHeight(), 0)
	case "g", "home":
		m.scroll = 0
	case "G", "end":
		m.scroll = m.describeMax()
	case "l":
		return m.open(logsScreen, m.current)
	default:
		return m.actionKey(k, m.current)
	}

	return m, nil
}

func (m Model) logsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.following = false
		m.scroll = min(m.scroll+1, m.logsMax())
	case "down", "j":
		m.scroll = max(m.scroll-1, 0)
	case "pgup", "ctrl+b":
		m.following = false
		m.scroll = min(m.scroll+m.logsPage(), m.logsMax())
	case "pgdown", "ctrl+f":
		m.scroll = max(m.scroll-m.logsPage(), 0)
	case "g", "home":
		m.following = false
		m.scroll = m.logsMax()
	case "G", "end":
		m.scroll = 0
		m.following = true
	case "s":
		m.following = !m.following

		if m.following {
			m.scroll = 0
		}
	case "d":
		return m.open(describeScreen, m.current)
	default:
		return m.actionKey(k, m.current)
	}

	return m, nil
}

func (m Model) confirmKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "left", "right", "tab", "shift+tab", "h":
		m.confirm.ok = !m.confirm.ok
	case "y":
		m.confirm.ok = true
	case "n", "esc":
		m.confirm = nil
	case "enter":
		action := m.confirm.action
		ok := m.confirm.ok
		m.confirm = nil

		if ok {
			return m, action
		}
	}

	return m, nil
}

// The commands ":" takes, k9s's way of moving between screens.
var commands = map[string]screen{
	"previews": previewsScreen,
	"pr":       previewsScreen,
	"describe": describeScreen,
	"logs":     logsScreen,
}

func (m Model) promptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.prompt = 0

		return m, nil
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case "enter":
		p, in := m.prompt, strings.TrimSpace(m.input)
		m.prompt = 0

		if p == '/' {
			m.filter = in
			m.cursor = 0

			return m, nil
		}

		return m.command(in)
	default:
		if msg.Text != "" {
			m.input += msg.Text
		}
	}

	if m.prompt == '/' {
		m.filter = m.input
		m.cursor = 0
	}

	return m, nil
}

func (m Model) command(in string) (tea.Model, tea.Cmd) {
	switch in {
	case "q", "q!", "quit":
		return m, tea.Quit
	case "":
		return m, nil
	}

	s, ok := commands[in]

	if !ok {
		m.say(fmt.Sprintf("`%s` isn't a command: previews, describe, logs or quit.", in), true)

		return m, nil
	}

	if s == previewsScreen {
		m.screens = []screen{previewsScreen}
		m.current = nil

		return m, nil
	}

	p := m.current

	if p == nil {
		p = m.selected()
	}

	return m.open(s, p)
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		name := "xdg-open"

		switch runtime.GOOS {
		case "darwin":
			name = "open"
		case "windows":
			name = "explorer"
		}

		if err := exec.Command(name, url).Start(); err != nil {
			return doneMsg{err: fmt.Errorf("couldn't open %s: %w", url, err)}
		}

		return nil
	}
}
