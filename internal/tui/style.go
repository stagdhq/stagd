package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// k9s's default skin: orange labels and logo, blue keys, an aqua table
// whose rows are colored by how the preview is doing.
var (
	orange  = lipgloss.Color("#FFA500")
	aqua    = lipgloss.Color("#00FFFF")
	sky     = lipgloss.Color("#87CEFA")
	blue    = lipgloss.Color("#1E90FF")
	white   = lipgloss.Color("#FFFFFF")
	gray    = lipgloss.Color("#808080")
	slate   = lipgloss.Color("#778899")
	red     = lipgloss.Color("#FF4500")
	yellow  = lipgloss.Color("#FFD700")
	green   = lipgloss.Color("#ADFF2F")
	purple  = lipgloss.Color("#9370DB")
	fuchsia = lipgloss.Color("#FF00FF")
	steel   = lipgloss.Color("#4682B4")
	papaya  = lipgloss.Color("#FFEFD5")
	black   = lipgloss.Color("#000000")

	labelStyle  = lipgloss.NewStyle().Foreground(orange).Bold(true)
	valueStyle  = lipgloss.NewStyle().Foreground(white).Bold(true)
	keyStyle    = lipgloss.NewStyle().Foreground(blue).Bold(true)
	hintStyle   = lipgloss.NewStyle().Foreground(gray)
	logoStyle   = lipgloss.NewStyle().Foreground(orange).Bold(true)
	borderStyle = lipgloss.NewStyle().Foreground(blue)
	titleStyle  = lipgloss.NewStyle().Foreground(aqua).Bold(true)
	scopeStyle  = lipgloss.NewStyle().Foreground(fuchsia).Bold(true)
	countStyle  = lipgloss.NewStyle().Foreground(white).Bold(true)
	headerStyle = lipgloss.NewStyle().Foreground(white).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(gray)
	fieldStyle  = lipgloss.NewStyle().Foreground(steel)
	textStyle   = lipgloss.NewStyle().Foreground(papaya)
	errStyle    = lipgloss.NewStyle().Foreground(red)
	okStyle     = lipgloss.NewStyle().Foreground(aqua)

	crumbStyle  = lipgloss.NewStyle().Foreground(black).Background(aqua).Padding(0, 1)
	activeCrumb = lipgloss.NewStyle().Foreground(black).Background(orange).Padding(0, 1).Bold(true)
	promptStyle = lipgloss.NewStyle().Foreground(white)
	cursorStyle = lipgloss.NewStyle().Foreground(black).Background(aqua)
	buttonStyle = lipgloss.NewStyle().Foreground(black).Background(sky).Padding(0, 1)
	pressStyle  = lipgloss.NewStyle().Foreground(black).Background(orange).Padding(0, 1).Bold(true)
)

// statusColor is a row's color: k9s's for healthy, pending, failing and gone.
func statusColor(s apiv1.PreviewStatus) lipgloss.Style {
	switch s {
	case apiv1.PreviewStatus_PREVIEW_STATUS_LIVE:
		return lipgloss.NewStyle().Foreground(sky)
	case apiv1.PreviewStatus_PREVIEW_STATUS_FAILED:
		return lipgloss.NewStyle().Foreground(red)
	case apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING:
		return lipgloss.NewStyle().Foreground(slate)
	case apiv1.PreviewStatus_PREVIEW_STATUS_DESTROYED:
		return lipgloss.NewStyle().Foreground(purple)
	default:
		return lipgloss.NewStyle().Foreground(orange)
	}
}

// label turns an enum's name into a word: PREVIEW_STATUS_LIVE is Live.
func label(name, prefix string) string {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, prefix), "_", " "))

	if s == "" || s == "unspecified" {
		return "—"
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

func statusLabel(s apiv1.PreviewStatus) string {
	return label(s.String(), "PREVIEW_STATUS_")
}

// age is how long ago, as k9s says it: 45s, 12m, 3h, 6d.
func age(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}

	d := now.Sub(t)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// what a preview runs: #482, or the branch when there's no pull request.
func what(p *apiv1.Preview) string {
	if n := p.GetChange().GetNumber(); n > 0 {
		return fmt.Sprintf("#%d", n)
	}

	return p.GetBranch()
}

// title is the pull request's, or the branch's commit when there's none.
func title(p *apiv1.Preview) string {
	if t := p.GetChange().GetTitle(); t != "" {
		return t
	}

	if sha := p.GetSha(); sha != "" {
		return "@ " + sha[:min(7, len(sha))]
	}

	return ""
}

var serviceColors = []lipgloss.Style{
	lipgloss.NewStyle().Foreground(aqua),
	lipgloss.NewStyle().Foreground(fuchsia),
	lipgloss.NewStyle().Foreground(blue),
	lipgloss.NewStyle().Foreground(green),
	lipgloss.NewStyle().Foreground(yellow),
	lipgloss.NewStyle().Foreground(sky),
}

// serviceStyle gives a service the same color every time.
func serviceStyle(service string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(service))

	return serviceColors[h.Sum32()%uint32(len(serviceColors))]
}

var levelStyles = map[apiv1.LogLevel]lipgloss.Style{
	apiv1.LogLevel_LOG_LEVEL_DEBUG: dimStyle,
	apiv1.LogLevel_LOG_LEVEL_INFO:  lipgloss.NewStyle().Foreground(green),
	apiv1.LogLevel_LOG_LEVEL_WARN:  lipgloss.NewStyle().Foreground(yellow),
	apiv1.LogLevel_LOG_LEVEL_ERROR: errStyle.Bold(true),
}
