package mapview

import (
	"math"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"
)

const panFrameInterval = time.Second / 60

type panMotion struct {
	active               bool
	serial               uint64
	x, y, vx, vy, tx, ty float64
	world                float64
	last                 time.Time
	key                  renderKey
}

type panAnimationMsg struct {
	owner  *panState
	serial uint64
}

func (m *Model) arrowPan(dx, dy float64) (Model, tea.Cmd) {
	if !m.smoothPan || m.panBufferTiles == 0 {
		m.panPixels(dx, dy)
		cmd := m.renderMapCmd()
		return *m, cmd
	}
	a := &m.panState.motion
	key := m.currentRenderKey()
	start := !a.active || a.key != key
	if start {
		a.serial++
		a.active = true
		a.x, a.y, a.world = worldPixels(m.lat, m.lng, m.zoom, m.tileProvider.TileSize)
		a.tx, a.ty = a.x, a.y
		a.vx, a.vy = 0, 0
		a.last, a.key = time.Now(), key
	}
	factor := float64(opticalCropFactor(m.opticalZoom))
	// Keep X unwrapped so crossing the dateline takes the short path.
	a.tx += dx / factor
	a.ty = max(0, min(a.world, a.ty+dy/factor))
	if start {
		return *m, m.panAnimationTick()
	}
	return *m, nil
}

func (m *Model) panAnimationTick() tea.Cmd {
	owner, serial := m.panState, m.panState.motion.serial
	return tea.Tick(panFrameInterval, func(time.Time) tea.Msg {
		return panAnimationMsg{owner: owner, serial: serial}
	})
}

func (m Model) updatePanAnimation(msg panAnimationMsg) (Model, tea.Cmd) {
	if msg.owner != m.panState || msg.serial != m.panState.motion.serial {
		return m, nil
	}
	a := &m.panState.motion
	if !a.active {
		return m, nil
	}
	// Stop animating if the zoom, location, size, or style changed.
	if a.key != m.currentRenderKey() {
		a.active = false
		return m, nil
	}
	now := time.Now()
	m.advancePanAnimation(now.Sub(a.last).Seconds())
	a.last = now
	cmd := m.renderMapCmd()
	if a.active {
		cmd = tea.Batch(cmd, m.panAnimationTick())
	}
	return m, cmd
}

func (m *Model) advancePanAnimation(dt float64) {
	a := &m.panState.motion
	// Use elapsed time to keep animation speed independent of frame rate.
	spring := harmonica.NewSpring(dt, 24, 1)
	a.x, a.vx = spring.Update(a.x, a.vx, a.tx)
	a.y, a.vy = spring.Update(a.y, a.vy, a.ty)
	if math.Hypot(a.tx-a.x, a.ty-a.y) < .02 && math.Hypot(a.vx, a.vy) < .5 {
		a.x, a.y = a.tx, a.ty
		a.vx, a.vy, a.active = 0, 0, false
	}
	x := math.Mod(math.Mod(a.x, a.world)+a.world, a.world)
	m.lng = x/a.world*360 - 180
	m.lat = math.Atan(math.Sinh(math.Pi*(1-2*a.y/a.world))) * 180 / math.Pi
	a.key = m.currentRenderKey()
}
