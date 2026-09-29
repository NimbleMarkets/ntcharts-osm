package mapview

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	sm "github.com/flopp/go-staticmaps"
	"github.com/golang/geo/s2"
)

func bufferedTestModel() Model {
	m := NewWithConfig(Config{Cols: 20, Rows: 11, PanBuffer: 1})
	m.SetLatLng(0, 0, 15)
	m.tileProvider = sm.NewTileProviderNone()
	return m
}

func seedPanBuffer(m *Model) {
	key := m.currentRenderKey()
	w, h := targetMapDims(key.cols*osmPxPerCellW*key.oversample, key.rows*osmPxPerCellH*key.oversample, key.maxAspectRatio)
	padding := 256
	img := image.NewRGBA(image.Rect(0, 0, w+2*padding, h+2*padding))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x % 251), uint8(y % 251), 0, 255})
		}
	}
	x, y, world := worldPixels(key.lat, key.lng, key.zoom+log2(key.oversample), 256)
	m.panState.buffer = &panBuffer{key: key, img: img, centerX: x, centerY: y,
		world: world, mapW: w, mapH: h, padding: padding, tileSize: 256}
}

func TestBufferedPanUpdatesWithoutFetch(t *testing.T) {
	m := bufferedTestModel()
	seedPanBuffer(&m)
	if cmd := m.renderMapCmd(); cmd != nil {
		t.Fatal("interior buffer view unexpectedly dispatched work")
	}
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd != nil || m.panState.busy || m.inFlight() {
		t.Fatal("nearby pan should apply synchronously without fetching or loading")
	}
	got := color.RGBAModel.Convert(m.Image().At(0, 0)).(color.RGBA)
	want := color.RGBA{uint8((256 + 16) % 251), uint8(256 % 251), 0, 255}
	if got != want {
		t.Fatalf("panned source origin = %v, want %v", got, want)
	}
}

func TestBufferedInitPublishesImageThroughValueReceiver(t *testing.T) {
	m := bufferedTestModel()
	seedPanBuffer(&m)
	m.Init()
	if m.Image() == nil {
		t.Fatal("value-receiver Init lost the synchronously cropped image")
	}
	// Optical zoom should reuse the buffered image.
	gen := *m.renderGen
	if cmd := m.SetOpticalZoom(1); cmd != nil || *m.renderGen != gen {
		t.Fatal("optical zoom failed to reuse Init's buffered source")
	}
}

func TestBufferedPanPrefetchesBeforeEdge(t *testing.T) {
	m := bufferedTestModel()
	seedPanBuffer(&m)
	m.panPixels(144, 0)
	cmd := m.renderMapCmd()
	if cmd == nil || !m.panState.busy || m.sourceImage == nil || m.inFlight() {
		t.Fatal("near-edge view should display immediately and refill in the background")
	}
	for i := 0; i < 3; i++ {
		m.panPixels(16, 0)
		if next := m.renderMapCmd(); next != nil {
			t.Fatal("another tile render started while the first was active")
		}
	}
	lat, lng := m.Center()
	m, _ = m.Update(cmd())
	if gotLat, gotLng := m.Center(); gotLat != lat || gotLng != lng {
		t.Fatal("background completion reset the camera to its old target")
	}
	if m.inFlight() || m.panState.displayed != m.currentRenderKey() {
		t.Fatal("background result was not cropped for the latest position")
	}
}

func TestBufferedFetchCoalescesWithoutWaitingForSilence(t *testing.T) {
	m := bufferedTestModel()
	first := m.renderMapCmd()
	if first == nil || !m.panState.busy {
		t.Fatal("first request must start immediately")
	}
	for i := 0; i < 5; i++ {
		m.panPixels(16, 0)
		if cmd := m.renderMapCmd(); cmd != nil {
			t.Fatal("input spawned concurrent tile renders")
		}
	}
	m, _ = m.Update(first())
	if m.inFlight() || m.sourceImage == nil || m.panState.displayed != m.currentRenderKey() {
		t.Fatal("in-flight result did not satisfy the newer camera position")
	}

	// A scheduled fetch should use the latest position.
	m.panPixels(512, 0)
	m.panState.lastStart = time.Now()
	tick := m.renderMapCmd()
	if tick == nil || !m.panState.waiting {
		t.Fatal("expected a scheduled throttle tick")
	}
	for i := 0; i < 5; i++ {
		m.panPixels(16, 0)
		if cmd := m.renderMapCmd(); cmd != nil {
			t.Fatal("input replaced the already scheduled tick")
		}
	}
	m.panState.lastStart = time.Now().Add(-panRenderInterval)
	m, fetch := m.Update(panRenderTickMsg{owner: m.panState})
	if fetch == nil || !m.panState.busy || m.panState.waiting {
		t.Fatal("tick did not start a render during continued input")
	}
	msg := fetch().(bufferImageMsg)
	if msg.buffer.key != m.currentRenderKey() {
		t.Fatal("tick rendered an old captured target")
	}
}

func TestBufferRejectsChangedStyleAndOtherOwners(t *testing.T) {
	m := bufferedTestModel()
	first := m.renderMapCmd()
	msg := first().(bufferImageMsg)
	other := bufferedTestModel()
	other, cmd := other.Update(msg)
	if cmd != nil || other.panState.buffer != nil {
		t.Fatal("another model accepted this buffer")
	}
	m.tileStyle = CartoDark
	m.panState.lastStart = time.Now().Add(-panRenderInterval)
	m, cmd = m.Update(msg)
	if m.sourceImage != nil || cmd == nil || !m.panState.busy {
		t.Fatal("stale style must not display; the latest style must be fetched")
	}
}

func TestBackgroundRefillErrorKeepsVisibleMap(t *testing.T) {
	m := bufferedTestModel()
	seedPanBuffer(&m)
	m.panPixels(144, 0)
	m.renderMapCmd()
	visible := m.Image()
	m, cmd := m.Update(bufferImageMsg{owner: m.panState,
		buffer: &panBuffer{key: m.currentRenderKey()}, err: errors.New("refill failed")})
	if m.errMsg != "" || m.Image() != visible || cmd != nil || m.panState.busy {
		t.Fatal("failed refill hid the visible map or caused a retry loop")
	}
}

func TestBufferCropAcrossDateline(t *testing.T) {
	m := bufferedTestModel()
	m.SetLatLng(0, 179.9999, 15)
	seedPanBuffer(&m)
	m.panPixels(16, 0)
	if m.lng > 0 {
		t.Fatal("expected eastward pan to cross the dateline")
	}
	r, ok := m.panState.buffer.viewport(m.currentRenderKey())
	if !ok || r.Min.X != 272 {
		t.Fatalf("dateline crop = %v, covered %v", r, ok)
	}
}

func TestPanUsesScreenPixelsAtDifferentLatitudes(t *testing.T) {
	for _, lat := range []float64{0, 60, 80} {
		m := bufferedTestModel()
		m.SetLatLng(lat, 10, 15)
		x, y, _ := worldPixels(m.lat, m.lng, m.zoom, 256)
		m.panPixels(16, -16)
		nx, ny, _ := worldPixels(m.lat, m.lng, m.zoom, 256)
		if math.Abs(nx-x-16) > 1e-6 || math.Abs(ny-y+16) > 1e-6 {
			t.Fatalf("latitude %g: pan moved (%g,%g) pixels", lat, nx-x, ny-y)
		}
	}
	m := bufferedTestModel()
	m.SetLatLng(maxMercatorLatitude, 0, 15)
	m.panPixels(0, -16)
	if math.Abs(m.lat-maxMercatorLatitude) > 1e-9 {
		t.Fatal("northward pan should clamp at the Mercator boundary")
	}
}

func TestBufferedKittyPresentsFramesDuringRapidInput(t *testing.T) {
	forceKittySupported(t)
	m := bufferedTestModel()
	m.pic.Toggle()
	seedPanBuffer(&m)
	first := m.renderMapCmd()
	for i := 0; i < 5; i++ {
		m.panPixels(16, 0)
		if cmd := m.renderMapCmd(); cmd != nil {
			t.Fatal("pan invalidated the encoding in flight")
		}
	}
	frame := first().(panPictureMsg).msg.(picture.KittyFrameMsg)
	if cmd := m.pic.Update(frame); cmd == nil {
		t.Fatal("first frame became stale during input")
	}
	// Bubble Tea sends this after the raw image and placement sequence.
	m, latest := m.Update(panPresentedMsg{owner: m.panState})
	if latest == nil {
		t.Fatal("latest requested image was not queued after presentation")
	}
	next := latest().(panPictureMsg).msg.(picture.KittyFrameMsg)
	if next.APC == frame.APC || m.pic.Update(next) == nil {
		t.Fatal("latest pan was not encoded and accepted")
	}
}

type countingImage struct {
	image.Image
	reads atomic.Int64
}

func (i *countingImage) At(x, y int) color.Color {
	i.reads.Add(1)
	return i.Image.At(x, y)
}

func TestGlyphCacheSurvivesValueReceiverView(t *testing.T) {
	m := New(20, 11)
	src := &countingImage{Image: newSolidImage(color.RGBA{90, 40, 30, 255})}
	m.pic.SetImage(src)
	first := m.View().Content
	reads := src.reads.Load()
	if reads == 0 {
		t.Fatal("first view did not render the source")
	}
	if m.View().Content != first || src.reads.Load() != reads {
		t.Fatal("unchanged View re-rendered the source instead of using the glyph cache")
	}
}

func TestSafeMarkerPreservesPinOverlappingViewport(t *testing.T) {
	ctx := sm.NewContext()
	ctx.SetTileProvider(sm.NewTileProviderNone())
	ctx.SetCenter(s2.LatLngFromDegrees(0, 0))
	ctx.SetZoom(16)
	ctx.SetSize(128, 128)
	trans, err := ctx.Transformer()
	if err != nil {
		t.Fatal(err)
	}
	cx, cy := trans.LatLngToXY(s2.LatLngFromDegrees(0, 0))
	// The pin sits just outside the image with its right edge still visible.
	// Older go-staticmaps versions wrap it out of view.
	ll := trans.XYToLatLng(cx-64-4, cy)
	addMapMarkers(ctx, []Marker{{Lat: ll.Lat.Degrees(), Lng: ll.Lng.Degrees()}}, 0, 0, 16, 256)
	img, err := ctx.Render()
	if err != nil {
		t.Fatal(err)
	}
	for y := 32; y < 64; y++ {
		for x := 0; x < 8; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > g && r > b {
				return
			}
		}
	}
	t.Fatal("partially visible pin was lost at the left edge")
}

func BenchmarkBufferedPan(b *testing.B) {
	m := bufferedTestModel()
	seedPanBuffer(&m)
	m.renderMapCmd()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dx := 16.0
		if i%2 != 0 {
			dx = -16
		}
		m.panPixels(dx, 0)
		m.renderMapCmd()
	}
}
