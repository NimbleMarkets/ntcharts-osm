package mapview

import (
	"math"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSpringPanSettlesWithoutOvershoot(t *testing.T) {
	m := bufferedTestModel()
	m.smoothPan = true
	seedPanBuffer(&m)
	m.renderMapCmd()
	x, _, _ := worldPixels(m.lat, m.lng, m.zoom, 256)
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd == nil || m.lng != 0 {
		t.Fatal("arrow should schedule animation without jumping the camera")
	}
	previous := x
	for i := 0; i < 120 && m.panState.motion.active; i++ {
		m.advancePanAnimation(1.0 / 60)
		position := m.panState.motion.x
		if position < previous || position > x+16 {
			t.Fatalf("non-monotonic motion: %f -> %f", previous, position)
		}
		previous = position
		if cmd := m.renderMapCmd(); cmd != nil || m.panState.busy {
			t.Fatal("interior animation fetched tiles")
		}
	}
	if m.panState.motion.active || math.Abs(previous-x-16) > 1e-6 {
		t.Fatal("spring did not settle exactly at the target")
	}
}

func TestSpringRetargetKeepsVelocityAndOneTimer(t *testing.T) {
	m := bufferedTestModel()
	m.smoothPan = true
	m, _ = m.arrowPan(16, 0)
	m.advancePanAnimation(1.0 / 60)
	v, serial, target := m.panState.motion.vx, m.panState.motion.serial, m.panState.motion.tx
	m, cmd := m.arrowPan(16, 0)
	if cmd != nil || m.panState.motion.serial != serial || m.panState.motion.vx != v || m.panState.motion.tx != target+16 {
		t.Fatal("repeat reset velocity, lost distance, or started a second timer")
	}
	m, _ = m.arrowPan(-16, 0)
	if m.panState.motion.tx != target || m.panState.motion.vx != v {
		t.Fatal("direction reversal should retarget without a velocity discontinuity")
	}
}

func TestSpringDatelineAndLatitudeClamp(t *testing.T) {
	m := bufferedTestModel()
	m.smoothPan = true
	m.SetLatLng(maxMercatorLatitude, 179.9999, 15)
	m, _ = m.arrowPan(16, -16)
	for i := 0; i < 120 && m.panState.motion.active; i++ {
		m.advancePanAnimation(1.0 / 60)
	}
	if m.lng > -179.99 || math.Abs(m.lat-maxMercatorLatitude) > 1e-8 {
		t.Fatalf("dateline or pole handling failed: %f, %f", m.lat, m.lng)
	}
}

func TestSpringTicksRouteAndCancelOnNewLocation(t *testing.T) {
	m := bufferedTestModel()
	m.smoothPan = true
	seedPanBuffer(&m)
	m, _ = m.arrowPan(16, 0)
	msg := panAnimationMsg{owner: m.panState, serial: m.panState.motion.serial}
	if !IsMapOwnMsg(msg) {
		t.Fatal("animation tick must route to mapview")
	}
	m.panState.motion.last = time.Now().Add(-panFrameInterval)
	m, cmd := m.Update(msg)
	if cmd == nil || m.lng <= 0 {
		t.Fatal("tick did not advance and schedule the next frame")
	}
	m.SetLatLng(40, 10, 10)
	m, cmd = m.Update(msg)
	if cmd != nil || m.panState.motion.active || m.lat != 40 || m.lng != 10 {
		t.Fatal("old animation moved a newly selected location")
	}
	m, _ = m.arrowPan(16, 0)
	m, cmd = m.Update(msg)
	if cmd != nil || !m.panState.motion.active || m.lng != 10 {
		t.Fatal("stale tick affected a newer animation")
	}
}
