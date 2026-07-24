package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	gboomTitle = iota
	gboomPlaying
	gboomWon
	gboomDead
)

const gboomFOV = math.Pi / 3

var gboomMap = []string{
	"################",
	"#..............#",
	"#..##..........#",
	"#..........##..#",
	"#.....#........#",
	"#.....#........#",
	"#..............#",
	"################",
}

type gboomEnemy struct {
	x, y     float64
	hp       int
	cooldown float64
}

type gboomState struct {
	phase      int
	x, y       float64
	angle      float64
	hp         int
	kills      int
	enemies    []gboomEnemy
	lastMouseX int
}

type gboomTickEvent struct{ epoch uint64 }
type gboomMouseEvent struct {
	x     int
	fire  bool
	moved bool
}

func newGboomState() *gboomState {
	return &gboomState{
		phase: gboomTitle, x: 2.5, y: 1.5, hp: 100, lastMouseX: -1,
		enemies: []gboomEnemy{
			{x: 12.5, y: 1.5, hp: 3},
			{x: 12.5, y: 6.5, hp: 3},
			{x: 7.5, y: 6.5, hp: 3},
		},
	}
}

func gboomTick(epoch uint64) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return gboomTickEvent{epoch: epoch}
	})
}

func (m *model) openGboom() tea.Cmd {
	m.gboomEpoch++
	m.gboom = newGboomState()
	m.status = "gboom"
	m.scroll = 0
	return gboomTick(m.gboomEpoch)
}

func (m *model) handleGboomTick(event gboomTickEvent) tea.Cmd {
	if m.gboom == nil || event.epoch != m.gboomEpoch {
		return nil
	}
	m.gboom.step(0.1)
	return gboomTick(event.epoch)
}

func (m *model) handleGboomKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	game := m.gboom
	key, text := msg.Key(), strings.ToLower(msg.Key().Text)
	if key.Code == tea.KeyEsc || text == "q" {
		m.gboom = nil
		m.status = "ready"
		return m, nil
	}
	if game.phase != gboomPlaying {
		if game.phase == gboomTitle {
			game.phase = gboomPlaying
			m.status = "gboom · playing"
		} else {
			m.gboom = nil
			m.status = "ready"
		}
		return m, nil
	}
	switch {
	case key.Code == tea.KeyUp || text == "w":
		game.move(math.Cos(game.angle)*0.35, math.Sin(game.angle)*0.35)
	case key.Code == tea.KeyDown || text == "s":
		game.move(-math.Cos(game.angle)*0.28, -math.Sin(game.angle)*0.28)
	case text == "a":
		game.move(math.Sin(game.angle)*0.3, -math.Cos(game.angle)*0.3)
	case text == "d":
		game.move(-math.Sin(game.angle)*0.3, math.Cos(game.angle)*0.3)
	case key.Code == tea.KeyLeft:
		game.angle -= 0.18
	case key.Code == tea.KeyRight:
		game.angle += 0.18
	case key.Code == tea.KeyEnter || text == " ":
		game.fire()
	}
	return m, nil
}

func (g *gboomState) handleMouse(event gboomMouseEvent) {
	if g.phase != gboomPlaying {
		return
	}
	if event.fire {
		g.fire()
	}
	if !event.moved {
		return
	}
	delta := event.x - g.lastMouseX
	if g.lastMouseX >= 0 && delta >= -12 && delta <= 12 {
		g.angle += float64(delta) * 0.06
	}
	g.lastMouseX = event.x
}

func (g *gboomState) move(dx, dy float64) {
	if !gboomBlocked(g.x+dx, g.y) {
		g.x += dx
	}
	if !gboomBlocked(g.x, g.y+dy) {
		g.y += dy
	}
}

func gboomBlocked(x, y float64) bool {
	cellX, cellY := int(x), int(y)
	return cellY < 0 || cellY >= len(gboomMap) || cellX < 0 || cellX >= len(gboomMap[cellY]) || gboomMap[cellY][cellX] != '.'
}

func (g *gboomState) fire() {
	target, targetDistance := -1, math.MaxFloat64
	for index := range g.enemies {
		enemy := &g.enemies[index]
		if enemy.hp <= 0 {
			continue
		}
		dx, dy := enemy.x-g.x, enemy.y-g.y
		distance := math.Hypot(dx, dy)
		angle := gboomAngle(math.Atan2(dy, dx) - g.angle)
		if math.Abs(angle) < math.Atan(0.35/distance) && distance < targetDistance && gboomVisible(g.x, g.y, enemy.x, enemy.y) {
			target, targetDistance = index, distance
		}
	}
	if target < 0 {
		return
	}
	g.enemies[target].hp--
	if g.enemies[target].hp == 0 {
		g.kills++
		if g.kills == len(g.enemies) {
			g.phase = gboomWon
		}
	}
}

func (g *gboomState) step(dt float64) {
	if g.phase != gboomPlaying {
		return
	}
	for index := range g.enemies {
		enemy := &g.enemies[index]
		if enemy.hp <= 0 {
			continue
		}
		enemy.cooldown = math.Max(enemy.cooldown-dt, 0)
		dx, dy := g.x-enemy.x, g.y-enemy.y
		distance := math.Hypot(dx, dy)
		if distance > 1.0 && distance < 9 && gboomVisible(enemy.x, enemy.y, g.x, g.y) {
			step := 0.9 * dt
			nx, ny := enemy.x+dx/distance*step, enemy.y+dy/distance*step
			if !gboomBlocked(nx, enemy.y) {
				enemy.x = nx
			}
			if !gboomBlocked(enemy.x, ny) {
				enemy.y = ny
			}
		} else if distance <= 1.0 && enemy.cooldown == 0 {
			g.hp = max(g.hp-7, 0)
			enemy.cooldown = 0.9
			if g.hp == 0 {
				g.phase = gboomDead
				return
			}
		}
	}
}

func gboomVisible(x0, y0, x1, y1 float64) bool {
	distance := math.Hypot(x1-x0, y1-y0)
	for travelled := 0.1; travelled < distance; travelled += 0.1 {
		ratio := travelled / distance
		if gboomBlocked(x0+(x1-x0)*ratio, y0+(y1-y0)*ratio) {
			return false
		}
	}
	return true
}

func (g *gboomState) lines(width, height int, colors themePalette) []string {
	if width < 30 || height < 8 {
		return []string{"GBOOM", "", "Terminal too small. Resize to at least 30×8.", "", "Esc quit"}
	}
	if g.phase == gboomTitle {
		return centeredGboomLines(width, height, []string{
			" ██████╗ ██████╗  ██████╗  ██████╗ ███╗   ███╗",
			"██╔════╝ ██╔══██╗██╔═══██╗██╔═══██╗████╗ ████║",
			"██║  ███╗██████╔╝██║   ██║██║   ██║██╔████╔██║",
			"╚██████╔╝██████╔╝╚██████╔╝╚██████╔╝██║╚██╔╝██║",
			" ╚═════╝ ╚═════╝  ╚═════╝  ╚═════╝ ╚═╝ ╚═╝ ╚═╝",
			"",
			"Press any key to play · Esc quit",
		}, colors.error)
	}

	rows, zbuffer := make([][]rune, height), make([]float64, width)
	for y := range rows {
		rows[y] = []rune(strings.Repeat(" ", width))
	}
	shades := []rune{'█', '▓', '▒', '░'}
	for x := 0; x < width; x++ {
		ray := g.angle - gboomFOV/2 + float64(x)/float64(width-1)*gboomFOV
		distance := 0.05
		for distance < 20 && !gboomBlocked(g.x+math.Cos(ray)*distance, g.y+math.Sin(ray)*distance) {
			distance += 0.05
		}
		distance *= math.Cos(ray - g.angle)
		zbuffer[x] = distance
		wallHeight := min(int(float64(height)/math.Max(distance, 0.1)), height)
		top, bottom := (height-wallHeight)/2, (height+wallHeight)/2
		shade := shades[min(int(distance/2.5), len(shades)-1)]
		for y := 0; y < height; y++ {
			switch {
			case y < top:
				rows[y][x] = '·'
			case y <= bottom:
				rows[y][x] = shade
			default:
				rows[y][x] = '.'
			}
		}
	}
	for _, enemy := range g.enemies {
		if enemy.hp <= 0 {
			continue
		}
		dx, dy := enemy.x-g.x, enemy.y-g.y
		distance := math.Hypot(dx, dy)
		angle := gboomAngle(math.Atan2(dy, dx) - g.angle)
		if math.Abs(angle) >= gboomFOV/2 || !gboomVisible(g.x, g.y, enemy.x, enemy.y) {
			continue
		}
		center := int((angle/gboomFOV + 0.5) * float64(width))
		size := max(int(float64(height)/distance), 1)
		for y := max(height/2-size/2, 0); y <= min(height/2+size/2, height-1); y++ {
			for x := max(center-size/3, 0); x <= min(center+size/3, width-1); x++ {
				if distance < zbuffer[x] {
					rows[y][x] = 'M'
				}
			}
		}
	}
	rows[height/2][width/2] = '+'
	lines := make([]string, height)
	for index := range rows {
		lines[index] = string(rows[index])
	}
	if g.phase == gboomWon {
		return centeredGboomLines(width, height, []string{"YOU SURVIVED", "", "Press any key to close"}, colors.positive)
	}
	if g.phase == gboomDead {
		return centeredGboomLines(width, height, []string{"YOU DIED", "", "Press any key to close"}, colors.error)
	}
	return lines
}

func centeredGboomLines(width, height int, text []string, color string) []string {
	lines := make([]string, height)
	top := max((height-len(text))/2, 0)
	for index, line := range text {
		line = truncate(line, width)
		lines[top+index] = strings.Repeat(" ", max((width-displayWidth(line))/2, 0)) + color + line + ansiReset
	}
	return lines
}

func gboomAngle(value float64) float64 {
	for value > math.Pi {
		value -= 2 * math.Pi
	}
	for value < -math.Pi {
		value += 2 * math.Pi
	}
	return value
}
