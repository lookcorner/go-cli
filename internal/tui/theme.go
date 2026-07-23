package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	uitheme "github.com/lookcorner/go-cli/internal/theme"
)

type themePalette struct {
	name     string
	title    string
	heading  string
	code     string
	list     string
	modal    string
	positive string
	warning  string
	error    string
}

func rgb(hex string) string {
	value, _ := strconv.ParseUint(hex, 16, 32)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", value>>16, value>>8&0xff, value&0xff)
}

func paletteFor(preference string) themePalette {
	name, ok := uitheme.Canonical(preference)
	if !ok {
		name = "groknight"
	}
	if name == "auto" {
		name = automaticTheme()
	}
	switch name {
	case "grokday":
		return themePalette{name: name, title: rgb("2457a6"), heading: rgb("2457a6"), code: rgb("087f8c"), list: rgb("8a5a00"), modal: rgb("7a3e9d"), positive: rgb("2c7a3f"), warning: rgb("8a5a00"), error: rgb("b42318")}
	case "tokyonight":
		return themePalette{name: name, title: rgb("7aa2f7"), heading: rgb("7aa2f7"), code: rgb("7dcfff"), list: rgb("e0af68"), modal: rgb("bb9af7"), positive: rgb("9ece6a"), warning: rgb("e0af68"), error: rgb("f7768e")}
	case "rosepine-moon":
		return themePalette{name: name, title: rgb("c4a7e7"), heading: rgb("c4a7e7"), code: rgb("9ccfd8"), list: rgb("f6c177"), modal: rgb("ebbcba"), positive: rgb("9ccfd8"), warning: rgb("f6c177"), error: rgb("eb6f92")}
	case "oscura-midnight":
		return themePalette{name: name, title: rgb("5ccfe6"), heading: rgb("5ccfe6"), code: rgb("aad94c"), list: rgb("ffb454"), modal: rgb("d2a6ff"), positive: rgb("aad94c"), warning: rgb("ffb454"), error: rgb("f07178")}
	default:
		return themePalette{name: "groknight", title: ansiCyan, heading: ansiCyan, code: ansiCyan, list: ansiYellow, modal: ansiYellow, positive: "\x1b[32m", warning: ansiYellow, error: "\x1b[31m"}
	}
}

func automaticTheme() string {
	background := strings.ToLower(strings.TrimSpace(os.Getenv("TERM_BACKGROUND")))
	if background == "light" {
		return "grokday"
	}
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if len(parts) > 0 {
		if color, err := strconv.Atoi(parts[len(parts)-1]); err == nil && color >= 7 {
			return "grokday"
		}
	}
	return "groknight"
}

func (m *model) colors() themePalette {
	if m.theme.name == "" {
		return paletteFor("groknight")
	}
	return m.theme
}

func (m *model) applyThemeCommand(value string) {
	preference := strings.TrimSpace(value)
	if preference == "" {
		current := m.colors().name
		for index, name := range uitheme.Names {
			if name == current {
				preference = uitheme.Names[(index+1)%len(uitheme.Names)]
				break
			}
		}
		if preference == "" {
			preference = uitheme.Names[0]
		}
	}
	canonical, ok := uitheme.Canonical(preference)
	if !ok {
		m.appendSystem("Unknown theme: " + preference + ". Available: auto, " + strings.Join(uitheme.Names[:], ", "))
		m.status = "theme argument invalid"
		return
	}
	previousName, previousTheme := m.themeName, m.theme
	m.themeName, m.theme = canonical, paletteFor(canonical)
	if m.persistTheme != nil {
		if err := m.persistTheme(canonical); err != nil {
			m.themeName, m.theme = previousName, previousTheme
			m.appendSystem("Couldn't save theme: " + err.Error())
			m.status = "theme update failed"
			return
		}
	}
	if canonical == "auto" {
		m.status = "theme: auto (" + m.theme.name + ")"
	} else {
		m.status = "theme: " + canonical
	}
}
