package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

var logo = []string{
	`     _                  _ `,
	` ___| |_ __ _  __ _  __| |`,
	`/ __| __/ _' |/ _' |/ _' |`,
	`\__ \ || (_| | (_| | (_| |`,
	`|___/\__\__,_|\__, |\__,_|`,
	`              |___/       `,
}

const headerHeight = 6

// The keys the header lists for each screen, as k9s does.
var hints = map[screen][][2]string{
	previewsScreen: {
		{"enter", "Describe"}, {"l", "Logs"}, {"o", "Open"}, {"p", "Pull request"}, {"w", "Wake"}, {"ctrl-d", "Destroy"},
		{"/", "Filter"}, {":", "Command"}, {"q", "Quit"},
	},
	describeScreen: {
		{"l", "Logs"}, {"o", "Open"}, {"p", "Pull request"}, {"w", "Wake"}, {"ctrl-d", "Destroy"}, {"esc", "Back"},
	},
	logsScreen: {
		{"s", "Autoscroll"}, {"G", "End"}, {"d", "Describe"}, {"o", "Open"}, {"ctrl-d", "Destroy"}, {"esc", "Back"},
	},
}

func (m Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.WindowTitle = "stagd · " + m.ctx.Org

	return v
}

// Render is the screen as text, with its colors.
func (m Model) Render() string {
	var b strings.Builder

	b.WriteString(m.header())
	b.WriteString("\n")

	if m.prompt != 0 {
		b.WriteString(m.promptBox())
		b.WriteString("\n")
	}

	t, lines := m.body()

	b.WriteString(m.box(t, lines))
	b.WriteString("\n")
	b.WriteString(m.crumbs())

	return b.String()
}

// bodyHeight is how many lines fit inside the box.
func (m Model) bodyHeight() int {
	h := m.height - headerHeight - 1 - 2

	if m.prompt != 0 {
		h -= 3
	}

	return max(h, 1)
}

func (m Model) header() string {
	live := 0

	for _, p := range m.previews {
		if p.GetStatus() == apiv1.PreviewStatus_PREVIEW_STATUS_LIVE {
			live++
		}
	}

	info := [][2]string{
		{"Org:", m.ctx.Org},
		{"User:", m.ctx.User},
		{"Stagd:", m.ctx.Server},
		{"CLI:", m.ctx.Version},
		{"Live:", fmt.Sprintf("%d of %d", live, len(m.previews))},
	}

	left := make([]string, headerHeight)

	for i, kv := range info {
		left[i] = " " + labelStyle.Render(fmt.Sprintf("%-7s", kv[0])) + valueStyle.Render(kv[1])
	}

	// The keys in columns of six, each as wide as its longest.
	hs := hints[m.top()]
	var cols [][]string

	for i := 0; i < len(hs); i += headerHeight {
		col := make([]string, headerHeight)
		w := 0

		for j := i; j < min(i+headerHeight, len(hs)); j++ {
			w = max(w, len(hs[j][0])+len(hs[j][1])+3)
		}

		for j := i; j < min(i+headerHeight, len(hs)); j++ {
			k := keyStyle.Render("<"+hs[j][0]+">") + " " + hintStyle.Render(hs[j][1])
			col[j-i] = k + strings.Repeat(" ", w-len(hs[j][0])-len(hs[j][1])-3)
		}

		for j := range col {
			if col[j] == "" {
				col[j] = strings.Repeat(" ", w)
			}
		}

		cols = append(cols, col)
	}

	leftWidth := 0

	for _, l := range left {
		leftWidth = max(leftWidth, lipgloss.Width(l))
	}

	leftWidth = max(leftWidth+4, 34)

	out := make([]string, headerHeight)

	for i := range out {
		line := pad(left[i], leftWidth)

		for _, col := range cols {
			line += col[i] + "    "
		}

		// The logo on the right, when there's room.
		if room := m.width - lipgloss.Width(line) - len(logo[i]) - 1; room >= 0 {
			line += strings.Repeat(" ", room) + logoStyle.Render(logo[i])
		}

		out[i] = ansi.Truncate(line, m.width, "")
	}

	return strings.Join(out, "\n")
}

func (m Model) promptBox() string {
	inner := m.width - 2
	text := " " + titleStyle.Render(string(m.prompt)) + " " + promptStyle.Render(m.input) + cursorStyle.Render(" ")

	return borderStyle.Render("┌"+strings.Repeat("─", inner)+"┐") + "\n" +
		borderStyle.Render("│") + pad(text, inner) + borderStyle.Render("│") + "\n" +
		borderStyle.Render("└"+strings.Repeat("─", inner)+"┘")
}

// box draws the body in a border with its title in the top line, k9s's frame.
func (m Model) box(title string, lines []string) string {
	inner := m.width - 2
	h := m.bodyHeight()

	tw := lipgloss.Width(title)
	left := max((inner-tw)/2, 0)
	right := max(inner-tw-left, 0)

	var b strings.Builder

	b.WriteString(borderStyle.Render("┌"+strings.Repeat("─", left)) + title + borderStyle.Render(strings.Repeat("─", right)+"┐") + "\n")

	for i := range h {
		l := ""

		if i < len(lines) {
			l = lines[i]
		}

		b.WriteString(borderStyle.Render("│") + pad(l, inner) + borderStyle.Render("│") + "\n")
	}

	b.WriteString(borderStyle.Render("└" + strings.Repeat("─", inner) + "┘"))

	return b.String()
}

func boxTitle(name, scope string, count int, extra string) string {
	t := " " + titleStyle.Render(name)

	if scope != "" {
		t += titleStyle.Render("(") + scopeStyle.Render(scope) + titleStyle.Render(")")
	}

	if count >= 0 {
		t += titleStyle.Render("[") + countStyle.Render(fmt.Sprint(count)) + titleStyle.Render("]")
	}

	if extra != "" {
		t += " " + scopeStyle.Render(extra)
	}

	return t + " "
}

func (m Model) body() (string, []string) {
	var t string
	var lines []string

	switch m.top() {
	case describeScreen:
		t, lines = m.describe()
	case logsScreen:
		t, lines = m.logLines()
	default:
		t, lines = m.table()
	}

	if m.confirm != nil {
		lines = m.dialog()
	}

	return t, lines
}

func (m Model) table() (string, []string) {
	ps := m.shown()
	extra := ""

	if m.filter != "" {
		extra = "</" + m.filter + ">"
	}

	t := boxTitle("Previews", m.ctx.Org, len(ps), extra)

	if !m.loaded {
		return t, center(m.width-2, m.bodyHeight(), dimStyle.Render("Loading previews…"))
	}

	if len(ps) == 0 {
		msg := "No previews. Open a pull request on a repository Stagd watches, or run `stagd preview create`."

		if m.filter != "" {
			msg = "None match /" + m.filter + ". esc clears the filter."
		}

		return t, center(m.width-2, m.bodyHeight(), dimStyle.Render(msg))
	}

	head := []string{"REPOSITORY", "OF", "TITLE", "STATUS", "COMPUTE", "AGE", "URL"}
	rows := make([][]string, len(ps))
	now := m.now()

	for i, p := range ps {
		rows[i] = []string{
			p.GetRepository().GetFullName(),
			what(p),
			title(p),
			statusLabel(p.GetStatus()),
			p.GetCompute().GetName(),
			age(p.GetCreateTime().AsTime(), now),
			p.GetUrl(),
		}

		if p.GetCreateTime() == nil {
			rows[i][5] = "—"
		}
	}

	// Each column as wide as its widest, the title at most 44, the URL what's left.
	widths := make([]int, len(head))

	for c := range head {
		widths[c] = len(head[c])

		for _, r := range rows {
			widths[c] = max(widths[c], lipgloss.Width(r[c]))
		}
	}

	widths[1] = min(widths[1], 18)
	widths[2] = min(widths[2], 44)
	inner := m.width - 2
	used := 0

	for c := range len(head) - 1 {
		used += widths[c] + 3
	}

	widths[len(head)-1] = max(inner-used-1, 8)

	line := func(cells []string) string {
		var b strings.Builder

		b.WriteString(" ")

		for c, cell := range cells {
			b.WriteString(pad(cell, widths[c]))

			if c < len(cells)-1 {
				b.WriteString("   ")
			}
		}

		return b.String()
	}

	h := m.bodyHeight() - 1
	first := 0

	if m.cursor >= h {
		first = m.cursor - h + 1
	}

	lines := []string{headerStyle.Render(line(head))}

	for i := first; i < min(len(rows), first+h); i++ {
		l := pad(line(rows[i]), inner)

		if i == m.cursor {
			lines = append(lines, cursorStyle.Render(l))
		} else {
			lines = append(lines, statusColor(ps[i].GetStatus()).Render(l))
		}
	}

	return t, lines
}

func (m Model) describe() (string, []string) {
	p := m.current
	t := boxTitle("Describe", p.GetRepository().GetFullName()+" "+what(p), -1, "")

	field := func(k, v string) string {
		return " " + fieldStyle.Render(fmt.Sprintf("%-12s", k+":")) + textStyle.Render(v)
	}

	var lines []string

	change := what(p)

	if c := p.GetChange(); c.GetNumber() > 0 {
		change = fmt.Sprintf("#%d %s (%s)", c.GetNumber(), c.GetTitle(), c.GetAuthor())
	}

	sha := p.GetSha()

	if len(sha) > 7 {
		sha = sha[:7]
	}

	lines = append(lines,
		field("Id", p.GetId()),
		field("Repository", p.GetRepository().GetFullName()),
		field("Change", change),
		field("Branch", p.GetBranch()+" @ "+sha),
		" "+fieldStyle.Render(fmt.Sprintf("%-12s", "Status:"))+statusColor(p.GetStatus()).Bold(true).Render(statusLabel(p.GetStatus())),
	)

	if p.GetError() != "" {
		lines = append(lines, " "+fieldStyle.Render(fmt.Sprintf("%-12s", "Error:"))+errStyle.Render(p.GetError()))
	}

	if p.GetWaiting() != "" {
		lines = append(lines, field("Waiting", p.GetWaiting()))
	}

	lines = append(lines,
		field("Compute", p.GetCompute().GetName()+" ("+label(p.GetCompute().GetProvider().String(), "COMPUTE_PROVIDER_")+")"),
		field("Started by", p.GetStartedBy()+", "+label(p.GetTrigger().String(), "PREVIEW_TRIGGER_")),
		field("Created", p.GetCreateTime().AsTime().Local().Format("2006-01-02 15:04")+" ("+age(p.GetCreateTime().AsTime(), m.now())+" ago)"),
	)

	if p.GetExpireTime() != nil {
		lines = append(lines, field("Expires", p.GetExpireTime().AsTime().Local().Format("2006-01-02 15:04")))
	}

	for _, s := range p.GetSeeds() {
		lines = append(lines, field("Seed", s.GetName()+" → "+s.GetDatabase()))
	}

	if len(p.GetIngresses()) > 0 {
		lines = append(lines, "", " "+fieldStyle.Render("URLs:"))

		for _, i := range p.GetIngresses() {
			mark := " "

			if i.GetPrimary() {
				mark = okStyle.Render("*")
			}

			who := "anyone with the link"

			if i.GetProtected() {
				who = "org members"
			}

			lines = append(lines, fmt.Sprintf("   %s %s %s  %s", mark, textStyle.Render(fmt.Sprintf("%-8s", i.GetName())), okStyle.Render(i.GetUrl()), dimStyle.Render(who)))
		}
	}

	if len(p.GetComponents()) > 0 {
		lines = append(lines, "", " "+fieldStyle.Render("Components:"))

		for _, c := range p.GetComponents() {
			phase := label(c.GetPhase().String(), "COMPONENT_PHASE_")
			style := textStyle

			switch {
			case strings.Contains(strings.ToLower(phase), "fail"):
				style = errStyle
			case strings.Contains(strings.ToLower(phase), "run") || strings.Contains(strings.ToLower(phase), "ready"):
				style = okStyle
			}

			lines = append(lines, fmt.Sprintf("   %s %s %s", textStyle.Render(fmt.Sprintf("%-10s", c.GetName())), style.Render(fmt.Sprintf("%-10s", phase)), dimStyle.Render(c.GetMessage())))
		}
	}

	if f := p.GetFailure(); f.GetCause() != "" {
		cause := "Cause"

		if f.GetConfidence() != apiv1.PreviewFailureConfidence_PREVIEW_FAILURE_CONFIDENCE_HIGH {
			cause = "Likely cause"
		}

		lines = append(lines, "", " "+errStyle.Bold(true).Render("Why it failed:"),
			"   "+fieldStyle.Render(cause+": ")+textStyle.Render(f.GetCause()),
			"   "+fieldStyle.Render("Fix: ")+textStyle.Render(f.GetFix()),
		)

		for _, e := range f.GetEvidence() {
			if e.GetSource() == "error" {
				continue
			}

			lines = append(lines, "     "+dimStyle.Render(e.GetSource()+" │ ")+e.GetText())
		}
	}

	if m.scroll > 0 {
		lines = lines[min(m.scroll, max(len(lines)-1, 0)):]
	}

	return t, lines
}

func (m Model) logLines() (string, []string) {
	p := m.current
	t := boxTitle("Logs", p.GetRepository().GetFullName()+" "+what(p), len(m.logs), "")

	on := okStyle.Render("On")

	if !m.following {
		on = dimStyle.Render("Off")
	}

	status := " " + fieldStyle.Render("Autoscroll:") + on

	h := m.bodyHeight() - 2
	end := max(len(m.logs)-m.scroll, 0)
	start := max(end-h, 0)

	lines := []string{status, ""}

	if len(m.logs) == 0 {
		lines = append(lines, center(m.width-2, h, dimStyle.Render("Waiting for lines…"))...)

		return t, lines
	}

	for _, l := range m.logs[start:end] {
		parts := []string{
			dimStyle.Render(l.GetTime().AsTime().Local().Format("15:04:05")),
			serviceStyle(l.GetService()).Render(fmt.Sprintf("%-8s", l.GetService())),
		}

		if l.GetLevel() != apiv1.LogLevel_LOG_LEVEL_UNSPECIFIED {
			parts = append(parts, levelStyles[l.GetLevel()].Render(fmt.Sprintf("%-5s", strings.TrimPrefix(l.GetLevel().String(), "LOG_LEVEL_"))))
		} else {
			parts = append(parts, "     ")
		}

		text := l.GetText()

		if l.GetLevel() == apiv1.LogLevel_LOG_LEVEL_ERROR {
			text = errStyle.Render(text)
		}

		lines = append(lines, " "+strings.Join(append(parts, text), " "))
	}

	return t, lines
}

// dialog is the confirmation, k9s's box in the middle with its two buttons.
func (m Model) dialog() []string {
	text := strings.Split(m.confirm.text, "\n")
	w := 0

	for _, l := range text {
		w = max(w, lipgloss.Width(l))
	}

	w = max(w+6, 36)

	cancel, ok := pressStyle.Render("Cancel"), buttonStyle.Render("OK")

	if m.confirm.ok {
		cancel, ok = buttonStyle.Render("Cancel"), pressStyle.Render("OK")
	}

	buttons := cancel + "   " + ok

	body := []string{
		borderStyle.Render("┌") + titleStyle.Render(" Confirm ") + borderStyle.Render(strings.Repeat("─", w-9)+"┐"),
		borderStyle.Render("│") + strings.Repeat(" ", w) + borderStyle.Render("│"),
	}

	for _, l := range text {
		body = append(body, borderStyle.Render("│")+lipgloss.PlaceHorizontal(w, lipgloss.Center, textStyle.Render(l))+borderStyle.Render("│"))
	}

	body = append(body,
		borderStyle.Render("│")+strings.Repeat(" ", w)+borderStyle.Render("│"),
		borderStyle.Render("│")+lipgloss.PlaceHorizontal(w, lipgloss.Center, buttons)+borderStyle.Render("│"),
		borderStyle.Render("└"+strings.Repeat("─", w)+"┘"),
	)

	return center(m.width-2, m.bodyHeight(), strings.Join(body, "\n"))
}

func (m Model) crumbs() string {
	names := map[screen]string{previewsScreen: "previews", describeScreen: "describe", logsScreen: "logs"}
	var parts []string

	for i, s := range m.screens {
		if i == len(m.screens)-1 {
			parts = append(parts, activeCrumb.Render(names[s]))
		} else {
			parts = append(parts, crumbStyle.Render(names[s]))
		}
	}

	line := " " + strings.Join(parts, " ")

	if m.flash != "" && m.now().Before(m.flashUntil) {
		style := okStyle

		if m.flashError {
			style = errStyle
		}

		line += "   " + style.Render(m.flash)
	}

	return ansi.Truncate(line, m.width, "…")
}

// pad cuts or fills s to w columns, colors and all.
func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")

	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// center puts text in the middle of a w by h space, as lines.
func center(w, h int, text string) []string {
	return strings.Split(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, text), "\n")
}
