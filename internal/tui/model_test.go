package tui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

func press(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(Model)
	}

	return m
}

var (
	up    = tea.KeyPressMsg{Code: tea.KeyUp}
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	pgUp  = tea.KeyPressMsg{Code: tea.KeyPgUp}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

func letter(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// withLogs is the logs screen of a preview with n lines, on a 30-line terminal.
func withLogs(t *testing.T, n int) Model {
	t.Helper()

	m := New(NewDemo(), Context{Org: "acme"})
	m.width, m.height = 120, 30

	ps, _ := m.src.Previews(context.Background())
	next, _ := m.Update(previewsMsg{previews: ps})
	m = press(next.(Model), letter('l'))

	lines := make([]*apiv1.LogLine, n)
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	for i := range lines {
		lines[i] = &apiv1.LogLine{Time: timestamppb.New(start.Add(time.Duration(i) * time.Second)), Service: "api", Text: fmt.Sprintf("line %d", i)}
	}

	next, _ = m.Update(logsMsg{id: m.current.GetId(), lines: lines})

	return next.(Model)
}

func TestLogsStopAtTheirFirstLine(t *testing.T) {
	m := withLogs(t, 100)
	most := 100 - m.logsPage()

	for range 200 {
		m = press(m, up)
	}

	if m.scroll != most {
		t.Fatalf("scrolled back %d lines, want %d: the first line at the top", m.scroll, most)
	}

	if m.following {
		t.Fatal("still following after scrolling back")
	}

	m = press(m, pgUp, pgUp)

	if m.scroll != most {
		t.Fatalf("a page up past the top scrolled to %d", m.scroll)
	}

	// Back down, it stops at the end too.
	for range 300 {
		m = press(m, down)
	}

	if m.scroll != 0 {
		t.Fatalf("scrolled to %d past the end", m.scroll)
	}
}

func TestFewerLogsThanFitDontScroll(t *testing.T) {
	m := press(withLogs(t, 5), up, up, pgUp)

	if m.scroll != 0 {
		t.Fatalf("5 lines scrolled by %d", m.scroll)
	}
}

func TestScrolledBackLogsStayPutAsLinesArrive(t *testing.T) {
	m := press(withLogs(t, 100), up, up, up)
	first := m.logs[len(m.logs)-m.scroll-1].GetText()

	more := []*apiv1.LogLine{{Time: timestamppb.Now(), Service: "web", Text: "new"}, {Time: timestamppb.Now(), Service: "web", Text: "newer"}}
	next, _ := m.Update(logsMsg{id: m.current.GetId(), lines: more})
	m = next.(Model)

	if got := m.logs[len(m.logs)-m.scroll-1].GetText(); got != first {
		t.Fatalf("the bottom line moved from %q to %q", first, got)
	}
}

func TestDescribeStopsAtItsLastLine(t *testing.T) {
	m := New(NewDemo(), Context{Org: "acme"})
	m.width, m.height = 120, 20

	ps, _ := m.src.Previews(context.Background())
	next, _ := m.Update(previewsMsg{previews: ps})
	m = press(next.(Model), down, down, enter)

	for range 100 {
		m = press(m, down)
	}

	if m.scroll != m.describeMax() || m.scroll == 0 {
		t.Fatalf("scrolled to %d, want its last line at the bottom (%d)", m.scroll, m.describeMax())
	}

	if m = press(m, esc); m.top() != previewsScreen {
		t.Fatal("esc didn't go back to the previews")
	}
}
