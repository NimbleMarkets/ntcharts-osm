package mapview

import (
	"image"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
)

func TestNewInitializesDefaultState(t *testing.T) {
	m := New(80, 24)

	if m.cols != 80 {
		t.Fatalf("expected cols 80, got %d", m.cols)
	}
	if m.rows != 24 {
		t.Fatalf("expected rows 24, got %d", m.rows)
	}
	if !m.initialized {
		t.Fatal("expected model to be initialized")
	}
	if m.zoom != 15 {
		t.Fatalf("expected default zoom 15, got %d", m.zoom)
	}
	if m.lat != 25.0782266 {
		t.Fatalf("expected default lat 25.0782266, got %f", m.lat)
	}
	if m.lng != -77.3383438 {
		t.Fatalf("expected default lng -77.3383438, got %f", m.lng)
	}
	if m.loc != "" {
		t.Fatalf("expected empty location, got %q", m.loc)
	}
	if m.osm == nil {
		t.Fatal("expected static map context to be initialized")
	}
	if m.tileProvider == nil {
		t.Fatal("expected tile provider to be initialized")
	}
	// Until the first render completes, View shows a centered "Loading…"
	// placeholder filling the cell rectangle so surrounding layout boxes
	// keep their full breadth.
	if got := m.View().Content; got == "" || !strings.Contains(got, "Loading") {
		t.Fatalf("expected loading placeholder, got %q", got)
	}
}

func TestSetSizeUpdatesDimsAndReturnsCmd(t *testing.T) {
	m := New(80, 24)

	cmd := m.SetSize(100, 30)
	if cmd == nil {
		t.Fatal("expected SetSize to return a render cmd when size changes")
	}
	if m.cols != 100 || m.rows != 30 {
		t.Fatalf("expected cols/rows 100/30, got %d/%d", m.cols, m.rows)
	}

	if cmd2 := m.SetSize(100, 30); cmd2 != nil {
		t.Fatal("expected SetSize with unchanged dims to return nil")
	}
}

func TestUpdateMapImageMsgFeedsPicture(t *testing.T) {
	m := New(80, 24)

	img := newSolidImage(color.RGBA{R: 100, G: 200, B: 50, A: 255})

	updated, _ := m.Update(mapImageMsg{img: img})
	if updated.errMsg != "" {
		t.Fatalf("expected no error message after successful image, got %q", updated.errMsg)
	}
	if updated.View().Content == "" {
		t.Fatal("expected non-empty view content after image set")
	}
}

func TestUpdateMapImageMsgWithErrorSetsErrMsg(t *testing.T) {
	m := New(80, 24)
	updated, _ := m.Update(mapImageMsg{err: errExample{}})
	if updated.errMsg == "" {
		t.Fatal("expected errMsg to be set when mapImageMsg carries error")
	}
	if updated.View().Content != updated.errMsg {
		t.Fatalf("expected View() to surface errMsg %q, got %q", updated.errMsg, updated.View().Content)
	}
}

func TestUpdateHandlesCoordinates(t *testing.T) {
	m := New(80, 24)

	updated, cmd := m.Update(MapCoordinates{Lat: 41.5, Lng: -72.7})
	if cmd == nil {
		t.Fatal("expected render command after coordinate update")
	}
	if updated.lat != 41.5 {
		t.Fatalf("expected lat 41.5, got %f", updated.lat)
	}
	if updated.lng != -72.7 {
		t.Fatalf("expected lng -72.7, got %f", updated.lng)
	}
	if updated.loc != "" {
		t.Fatalf("expected location to remain empty, got %q", updated.loc)
	}
}

func TestSetMarkersStoresAndClears(t *testing.T) {
	m := New(80, 24)

	m.SetMarkers([]Marker{
		{Lat: 41.5, Lng: -72.7},
		{Lat: 41.6, Lng: -72.8, Color: color.RGBA{0x00, 0xff, 0x00, 0xff}, Size: 20},
	})

	if len(m.markers) != 2 {
		t.Fatalf("expected 2 markers, got %d", len(m.markers))
	}
	if m.markers[0].Color != nil {
		t.Errorf("expected first marker to keep nil color for defaulting, got %#v", m.markers[0].Color)
	}
	if m.markers[1].Size != 20 {
		t.Errorf("expected second marker size 20, got %v", m.markers[1].Size)
	}

	m.ClearMarkers()
	if len(m.markers) != 0 {
		t.Fatalf("expected markers to be cleared, got %d", len(m.markers))
	}
}

func TestSetRenderModeTogglesAndReRenders(t *testing.T) {
	m := New(80, 24)

	if m.RenderMode() != RenderGlyph {
		t.Fatalf("expected default render mode RenderGlyph, got %v", m.RenderMode())
	}

	cmd := m.SetRenderMode(RenderKitty)
	if cmd == nil {
		t.Fatal("expected SetRenderMode to return a render cmd")
	}
	if m.RenderMode() != RenderKitty {
		t.Fatalf("expected RenderKitty after set, got %v", m.RenderMode())
	}

	cmd = m.SetRenderMode(RenderGlyph)
	if cmd == nil {
		t.Fatal("expected SetRenderMode to return a render cmd when going back to glyph")
	}
	if m.RenderMode() != RenderGlyph {
		t.Fatalf("expected RenderGlyph after toggle back, got %v", m.RenderMode())
	}
}

func TestUpdateZoomInRespectsUpperBound(t *testing.T) {
	m := New(80, 24)
	m.zoom = 16

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "+", Code: '+'}))
	if cmd == nil {
		t.Fatal("expected render command after zoom-in keypress")
	}
	if updated.zoom != 16 {
		t.Fatalf("expected zoom to stay capped at 16, got %d", updated.zoom)
	}
}

func TestIsMapUpdateRecognizesPictureMessages(t *testing.T) {
	if !IsMapUpdate(mapImageMsg{}) {
		t.Error("expected mapImageMsg to be a map update")
	}
	if !IsMapUpdate(MapCoordinates{}) {
		t.Error("expected MapCoordinates to be a map update")
	}
	if !IsMapUpdate(picture.KittyFrameMsg{}) {
		t.Error("expected picture.KittyFrameMsg to be a map update (forwarded to embedded pic.Model)")
	}
	if IsMapUpdate("random string") {
		t.Error("expected unrelated messages to not be a map update")
	}
}

type errExample struct{}

func (errExample) Error() string { return "render boom" }

func newSolidImage(c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
