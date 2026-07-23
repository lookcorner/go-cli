package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const debugFrameWindow = 120

type debugState struct {
	scrollHUD bool
	fpsHUD    bool
	frames    []time.Duration
	log       *os.File
	logPath   string
}

type scrollLogRecord struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Requested int    `json:"requested"`
	Applied   int    `json:"applied"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
	Maximum   int    `json:"maximum"`
	Viewport  int    `json:"viewport"`
}

func newDebugState() debugState {
	state := debugState{
		scrollHUD: envDebugEnabled("GROK_SCROLL_DEBUG"),
		fpsHUD:    envDebugEnabled("GROK_FPS"),
	}
	if raw, ok := os.LookupEnv("GROK_SCROLL_LOG"); ok && strings.TrimSpace(raw) != "0" {
		_, _ = state.enableLog(strings.TrimSpace(raw))
	}
	return state
}

func envDebugEnabled(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(value) != "0"
}

func (d *debugState) recordFrame(duration time.Duration) {
	if duration < 0 {
		return
	}
	d.frames = append(d.frames, duration)
	if len(d.frames) > debugFrameWindow {
		d.frames = d.frames[len(d.frames)-debugFrameWindow:]
	}
}

func (d *debugState) toggleLog() (string, bool, error) {
	if d.log != nil {
		err := d.closeLog()
		return "", false, err
	}
	path, err := d.enableLog("")
	return path, err == nil, err
}

func (d *debugState) closeLog() error {
	if d.log == nil {
		return nil
	}
	err := d.log.Close()
	d.log, d.logPath = nil, ""
	return err
}

func (d *debugState) enableLog(target string) (string, error) {
	path := target
	if path == "" || path == "1" {
		path = defaultScrollLogPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	d.log, d.logPath = file, path
	return path, nil
}

func defaultScrollLogPath() string {
	home := strings.TrimSpace(os.Getenv("GROK_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".grok")
		} else {
			home = ".grok"
		}
	}
	return filepath.Join(home, "logs", "scroll-log-"+time.Now().UTC().Format("20060102-150405.000")+".jsonl")
}

func (d *debugState) recordScroll(source string, requested, before, after, maximum, viewport int) {
	if d.log == nil {
		return
	}
	record := scrollLogRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Source: source,
		Requested: requested, Applied: after - before, Before: before, After: after,
		Maximum: maximum, Viewport: viewport,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, err = d.log.Write(append(data, '\n'))
	if err != nil {
		_ = d.log.Close()
		d.log, d.logPath = nil, ""
	}
}

func (d *debugState) status() string {
	return fmt.Sprintf("debug toggles: scroll %s, fps %s, log %s (/debug [scroll|fps|log])",
		onOff(d.scrollHUD), onOff(d.fpsHUD), onOff(d.log != nil))
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func (d *debugState) overlay(lines []string, width, scroll, maximum, viewport, content, wheelLines int, inverted, focused bool) []string {
	panels := make([]string, 0, 5)
	if d.scrollHUD {
		panels = append(panels,
			"scroll debug  (/debug scroll)",
			fmt.Sprintf("pos:%d/%d view:%d lines:%d", scroll, maximum, viewport, content),
			fmt.Sprintf("wheel:%d invert:%t focus:%t", wheelLines, inverted, focused),
		)
	}
	if d.fpsHUD {
		panels = append(panels, "fps debug  (/debug fps)", d.frameStats())
	}
	if len(panels) == 0 || len(lines) == 0 {
		return lines
	}
	panelWidth := min(44, width)
	leftWidth := max(width-panelWidth, 0)
	for index, panel := range panels {
		if index >= len(lines) {
			break
		}
		left := padRight(truncateANSIUnsafe(lines[index], leftWidth), leftWidth)
		lines[index] = left + "\x1b[7m" + padRight(truncate(panel, panelWidth), panelWidth) + ansiReset
	}
	return lines
}

func (d *debugState) frameStats() string {
	if len(d.frames) == 0 {
		return "fps:- p50:- p95:-"
	}
	values := append([]time.Duration(nil), d.frames...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var total time.Duration
	for _, value := range values {
		total += value
	}
	mean := total.Seconds() / float64(len(values))
	fps := 0.0
	if mean > 0 {
		fps = 1 / mean
	}
	p50 := values[(len(values)-1)*50/100].Seconds() * 1000
	p95 := values[(len(values)-1)*95/100].Seconds() * 1000
	return fmt.Sprintf("fps:%.0f p50:%.1fms p95:%.1fms", fps, p50, p95)
}

func (m *model) handleDebugCommand(command, argument string) {
	if command == "/scroll-debug" {
		argument = "scroll"
	}
	switch strings.TrimSpace(argument) {
	case "":
		m.appendSystem(m.debug.status())
		m.status = "debug status"
	case "scroll":
		m.debug.scrollHUD = !m.debug.scrollHUD
		m.status = "scroll debug: " + onOff(m.debug.scrollHUD)
	case "fps":
		m.debug.fpsHUD = !m.debug.fpsHUD
		m.status = "fps debug: " + onOff(m.debug.fpsHUD)
	case "log":
		path, enabled, err := m.debug.toggleLog()
		if err != nil {
			m.status = "scroll log: " + err.Error()
			return
		}
		if enabled {
			m.appendSystem("scroll log: recording to " + path)
			m.status = "scroll log: on"
		} else {
			m.appendSystem("scroll log: off")
			m.status = "scroll log: off"
		}
	default:
		m.appendSystem(fmt.Sprintf("Unknown /debug option %q. Usage: /debug [scroll|fps|log]", strings.TrimSpace(argument)))
		m.status = "debug option invalid"
	}
}
