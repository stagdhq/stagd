package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestScreens writes each screen with its colors to $TUI_SCREENS, to look at.
func TestScreens(t *testing.T) {
	dir := os.Getenv("TUI_SCREENS")

	if dir == "" {
		t.Skip("TUI_SCREENS not set")
	}

	demo := NewDemo()
	var m tea.Model = New(demo, Context{Org: "acme", User: "ada@acme.example", Server: "app.stagd.dev", Version: "2026.11.10"})

	run := func(msg tea.Msg) {
		var cmd tea.Cmd

		m, cmd = m.Update(msg)

		// Run what it asked for, once, as the program would.
		if cmd != nil {
			if out := cmd(); out != nil {
				if _, isTick := out.(tickMsg); !isTick {
					if b, ok := out.(tea.BatchMsg); ok {
						for _, c := range b {
							if c != nil {
								if o := c(); o != nil {
									if _, isTick := o.(tickMsg); !isTick {
										m, _ = m.Update(o)
									}
								}
							}
						}
					} else {
						m, _ = m.Update(out)
					}
				}
			}
		}
	}

	key := func(s string) tea.KeyPressMsg {
		switch s {
		case "enter":
			return tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			return tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			return tea.KeyPressMsg{Code: tea.KeyDown}
		case "ctrl+d":
			return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
		}

		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}

	save := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name+".ans"), []byte(m.(Model).Render()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(tea.WindowSizeMsg{Width: 130, Height: 34})
	ps, _ := demo.Previews(context.Background())
	run(previewsMsg{previews: ps})
	run(key("down"))
	save("1-previews")

	run(key("down"))
	run(key("enter"))
	save("2-describe")

	run(key("esc"))
	run(key("up"))
	run(key("up"))
	run(key("l"))
	save("3-logs")

	run(key("esc"))
	run(key("/"))
	for _, r := range "web" {
		run(key(string(r)))
	}
	save("4-filter")

	run(key("enter"))
	run(key("ctrl+d"))
	save("5-confirm")
}
