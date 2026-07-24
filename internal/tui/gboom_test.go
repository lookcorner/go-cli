package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestGboomCommandIsHiddenAndRequiresBareInvocation(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 70, height: 20}
	m.setInput("/gb")
	for _, item := range m.slashSuggestions() {
		if strings.Contains(item.label, "gboom") {
			t.Fatalf("hidden command was suggested: %#v", item)
		}
	}
	m.setInput("/gboom")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.gboom == nil || m.running || m.status != "gboom" {
		t.Fatalf("command=%v game=%#v running=%v status=%q", command != nil, m.gboom, m.running, m.status)
	}

	m = &model{ctx: context.Background(), runner: &agent.Runner{}, width: 70, height: 20}
	m.setInput("/gboom nightmare")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.gboom != nil || !m.running {
		t.Fatalf("arguments did not pass through: command=%v game=%#v running=%v", command != nil, m.gboom, m.running)
	}
}

func TestGboomStartsMovesFiresAndCloses(t *testing.T) {
	m := &model{gboom: newGboomState(), gboomEpoch: 1, width: 70, height: 20}
	updated, command := m.handleGboomKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.gboom.phase != gboomPlaying {
		t.Fatalf("command=%v phase=%d", command != nil, m.gboom.phase)
	}
	start := m.gboom.x
	updated, _ = m.handleGboomKey(tea.KeyPressMsg(tea.Key{Code: 'w', Text: "w"}))
	m = updated.(*model)
	if m.gboom.x <= start {
		t.Fatalf("player did not move: start=%v x=%v", start, m.gboom.x)
	}

	m.gboom.x, m.gboom.y, m.gboom.angle = 2.5, 1.5, 0
	m.gboom.enemies = []gboomEnemy{{x: 6.5, y: 1.5, hp: 3}}
	for range 3 {
		m.gboom.fire()
	}
	if m.gboom.kills != 1 || m.gboom.phase != gboomWon {
		t.Fatalf("kills=%d phase=%d enemy=%#v", m.gboom.kills, m.gboom.phase, m.gboom.enemies)
	}
	updated, _ = m.handleGboomKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if m.gboom != nil || m.status != "ready" {
		t.Fatalf("game=%#v status=%q", m.gboom, m.status)
	}
}

func TestGboomEnemiesChaseAttackAndStopAfterDeath(t *testing.T) {
	game := newGboomState()
	game.phase = gboomPlaying
	game.enemies = []gboomEnemy{{x: game.x + 0.5, y: game.y, hp: 3}}
	for range 200 {
		game.step(0.1)
		if game.phase != gboomPlaying {
			break
		}
	}
	if game.phase != gboomDead || game.hp != 0 {
		t.Fatalf("phase=%d hp=%d enemy=%#v", game.phase, game.hp, game.enemies)
	}
	before := game.enemies[0]
	game.step(1)
	if game.enemies[0] != before {
		t.Fatalf("dead game kept advancing: before=%#v after=%#v", before, game.enemies[0])
	}
}

func TestGboomRendersAndHandlesMouse(t *testing.T) {
	m := &model{
		gboom: newGboomState(), gboomEpoch: 2,
		width: 80, height: 24, workspace: "/work", modelName: "grok",
	}
	view := m.View()
	content := stripUIANSI(view.Content)
	if !strings.Contains(content, "GBOOM") || !strings.Contains(content, "Press any key to play") || strings.Contains(content, "Enter send") {
		t.Fatalf("title view=%q", content)
	}
	updated, _ := m.handleGboomKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	view = m.View()
	if !strings.Contains(stripUIANSI(view.Content), "KILLS 0/3") {
		t.Fatalf("playing view=%q", stripUIANSI(view.Content))
	}
	click := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 40, Y: 10, Button: tea.MouseLeft}))
	if click == nil {
		t.Fatal("click did not fire")
	}
	hp := m.gboom.enemies[0].hp
	updated, _ = m.Update(click())
	m = updated.(*model)
	if m.gboom.enemies[0].hp >= hp {
		t.Fatalf("click did not damage aimed enemy: before=%d after=%d", hp, m.gboom.enemies[0].hp)
	}
	motion := m.View().OnMouse(tea.MouseMotionMsg(tea.Mouse{X: 30, Y: 10, Button: tea.MouseNone}))
	updated, _ = m.Update(motion())
	m = updated.(*model)
	angle := m.gboom.angle
	motion = m.View().OnMouse(tea.MouseMotionMsg(tea.Mouse{X: 32, Y: 10, Button: tea.MouseNone}))
	updated, _ = m.Update(motion())
	m = updated.(*model)
	if m.gboom.angle <= angle {
		t.Fatalf("mouse did not turn player: before=%v after=%v", angle, m.gboom.angle)
	}
}

func TestGboomTickRejectsStaleGeneration(t *testing.T) {
	m := &model{gboom: newGboomState(), gboomEpoch: 4}
	m.gboom.phase = gboomPlaying
	before := m.gboom.enemies[0]
	if command := m.handleGboomTick(gboomTickEvent{epoch: 3}); command != nil || m.gboom.enemies[0] != before {
		t.Fatalf("stale tick advanced game: command=%v enemy=%#v", command != nil, m.gboom.enemies[0])
	}
	if command := m.handleGboomTick(gboomTickEvent{epoch: 4}); command == nil {
		t.Fatal("current tick did not continue")
	}
}

func TestGboomCollisionVisibilityAndSmallTerminal(t *testing.T) {
	game := newGboomState()
	game.x, game.y = 1.1, 1.5
	game.move(-0.3, 0)
	if game.x != 1.1 {
		t.Fatalf("player crossed a wall: x=%v", game.x)
	}
	if !gboomVisible(2.5, 1.5, 12.5, 1.5) {
		t.Fatal("open corridor was not visible")
	}
	if gboomVisible(2.5, 1.5, 4.5, 3.5) {
		t.Fatal("wall did not block line of sight")
	}
	lines := game.lines(20, 5, paletteFor("groknight"))
	if !strings.Contains(strings.Join(lines, "\n"), "Terminal too small") {
		t.Fatalf("small-terminal fallback=%q", lines)
	}
}
