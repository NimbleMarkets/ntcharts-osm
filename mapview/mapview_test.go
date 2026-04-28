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

	// gen 0 matches the freshly-constructed Model's renderGen of 0 so the
	// message is accepted without going through renderMapCmd first.
	updated, _ := m.Update(mapImageMsg{img: img})
	if updated.errMsg != "" {
		t.Fatalf("expected no error message after successful image, got %q", updated.errMsg)
	}
	if updated.View().Content == "" {
		t.Fatal("expected non-empty view content after image set")
	}
}

func TestUpdateMapImageMsgDropsStaleGen(t *testing.T) {
	m := New(80, 24)
	// Advance renderGen so an incoming msg with the old gen is "stale".
	if cmd := m.renderMapCmd(); cmd == nil {
		t.Fatal("expected renderMapCmd to return a Cmd")
	}
	staleImg := newSolidImage(color.RGBA{R: 50, G: 50, B: 50, A: 255})
	updated, cmd := m.Update(mapImageMsg{gen: 0, img: staleImg})
	if cmd != nil {
		t.Fatalf("expected stale msg to be ignored (no Cmd), got %v", cmd)
	}
	if updated.View().Content == "" || !strings.Contains(updated.View().Content, "Loading") {
		t.Fatal("expected stale msg not to flip the view away from the Loading placeholder")
	}
}

// TestInitBumpsSharedRenderGen pins the fix for the "places change → map
// doesn't redraw" bug: Init() has a value receiver, but the renderGen counter
// is heap-allocated so the bump survives the copy. Without the *uint64,
// Init's Cmd would carry a gen the live Model never sees, and every result
// would be filtered out as "stale".
func TestInitBumpsSharedRenderGen(t *testing.T) {
	m := New(80, 24)
	if m.renderGen == nil || m.lastAccepted == nil {
		t.Fatal("expected renderGen / lastAccepted to be heap-allocated")
	}
	if got := *m.renderGen; got != 0 {
		t.Fatalf("expected initial renderGen 0, got %d", got)
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init() should return a render Cmd when the Model is sized")
	}
	if got := *m.renderGen; got != 1 {
		t.Fatalf("expected renderGen to be bumped to 1 after Init, got %d", got)
	}
}

// TestInFlightBookkeeping pins the gen / lastAccepted accounting that drives
// the overlay branch in View: a freshly-accepted render leaves inFlight
// false, dispatching another render flips it true, and accepting that new
// render flips it back to false.
func TestInFlightBookkeeping(t *testing.T) {
	m := New(80, 24)

	img := newSolidImage(color.RGBA{R: 100, G: 200, B: 50, A: 255})
	updated, _ := m.Update(mapImageMsg{gen: 0, img: img})
	if updated.inFlight() {
		t.Fatal("inFlight should be false after the only render is accepted")
	}

	if cmd := updated.renderMapCmd(); cmd == nil {
		t.Fatal("expected renderMapCmd to return a Cmd")
	}
	if !updated.inFlight() {
		t.Fatal("expected inFlight to be true after dispatching a new render")
	}

	updated, _ = updated.Update(mapImageMsg{gen: *updated.renderGen, img: img})
	if updated.inFlight() {
		t.Fatal("inFlight should flip back to false once the latest render lands")
	}
}

// TestRenderMapCmdHitsCacheSynchronously verifies that a renderKey already
// present in the cache short-circuits renderMapCmd: no gen bump, no
// goroutine, no in-flight state — so the consumer doesn't see a Loading
// overlay flash when revisiting a known place.
func TestRenderMapCmdHitsCacheSynchronously(t *testing.T) {
	m := New(80, 24)

	// Pre-populate the cache with the entry that the current state would
	// look up.
	cachedImg := newSolidImage(color.RGBA{R: 1, G: 2, B: 3, A: 255})
	key := makeRenderKey(m.lat, m.lng, m.zoom, m.cols, m.picRows(), m.tileStyle, m.oversample, m.markers)
	m.cache.put(key, cachedImg)

	startGen := *m.renderGen

	// In glyph mode pic.SetImage returns nil (no Kitty frame to schedule),
	// so we don't assert on the Cmd's nil-ness — only on the absence of
	// the in-flight bookkeeping that would trigger the Loading overlay.
	_ = m.renderMapCmd()
	if got := *m.renderGen; got != startGen {
		t.Fatalf("expected cache hit to NOT bump renderGen (was %d, got %d)", startGen, got)
	}
	if m.inFlight() {
		t.Fatal("cache hit must not flip inFlight true")
	}
}

// TestFloorPow2 pins the snap-to-power-of-2 behavior used to normalize
// Config.Oversample.
func TestFloorPow2(t *testing.T) {
	cases := map[int]int{
		-3: 1, -1: 1, 0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 7: 4, 8: 8, 15: 8, 16: 16,
	}
	for in, want := range cases {
		if got := floorPow2(in); got != want {
			t.Errorf("floorPow2(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestOversample_CacheKeyDistinguishes verifies that two requests with
// different effective oversamples don't collide in the cache.
func TestOversample_CacheKeyDistinguishes(t *testing.T) {
	k1 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, nil)
	k2 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 2, nil)
	if k1 == k2 {
		t.Fatal("oversample 1 and 2 must produce different cache keys")
	}
}

// TestEffectiveOversample covers the maxOSMZoom cap math.
func TestEffectiveOversample(t *testing.T) {
	cases := []struct {
		name             string
		zoom, oversample int
		wantFactor       int
		wantBoost        int
	}{
		{"default", 10, 1, 1, 0},
		{"2x within budget", 10, 2, 2, 1},
		{"4x within budget", 10, 4, 4, 2},
		{"4x at cap-2", 17, 4, 4, 2}, // 17+2 = 19 ≤ 19
		{"4x at cap-1, halves to 2", 18, 4, 2, 1},
		{"4x at cap, halves to 1", 19, 4, 1, 0},
		{"2x at cap-1, fits", 18, 2, 2, 1},
		{"2x at cap, halves to 1", 19, 2, 1, 0},
		{"non-pow-2 floors first", 10, 5, 4, 2},
		{"zero treated as 1", 10, 0, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewWithConfig(Config{Cols: 80, Rows: 24, Oversample: tc.oversample})
			m.zoom = tc.zoom
			factor, boost := m.effectiveOversample()
			if factor != tc.wantFactor || boost != tc.wantBoost {
				t.Errorf("zoom=%d oversample=%d → got (factor=%d, boost=%d), want (%d, %d)",
					tc.zoom, tc.oversample, factor, boost, tc.wantFactor, tc.wantBoost)
			}
		})
	}
}

// TestNewWithConfig_DisableCache verifies CacheCap < 0 leaves the cache nil
// and forces every renderMapCmd through the goroutine path. Useful for
// consumers that want predictable memory or are already caching upstream.
func TestNewWithConfig_DisableCache(t *testing.T) {
	m := NewWithConfig(Config{Cols: 80, Rows: 24, CacheCap: -1})
	if m.cache != nil {
		t.Fatalf("expected cache to be nil with CacheCap=-1, got %#v", m.cache)
	}

	startGen := *m.renderGen
	if cmd := m.renderMapCmd(); cmd == nil {
		t.Fatal("expected non-nil Cmd when caching is disabled (goroutine path)")
	}
	if got := *m.renderGen; got != startGen+1 {
		t.Fatalf("expected renderGen bump (was %d, now %d)", startGen, got)
	}
}

// TestNewWithConfig_CustomCacheCap verifies an explicit positive CacheCap
// is honored.
func TestNewWithConfig_CustomCacheCap(t *testing.T) {
	m := NewWithConfig(Config{Cols: 80, Rows: 24, CacheCap: 4})
	if m.cache == nil {
		t.Fatal("expected cache to be allocated for CacheCap=4")
	}
	if m.cache.cap != 4 {
		t.Fatalf("expected cache cap 4, got %d", m.cache.cap)
	}
}

// TestRenderMapCmdMissBumpsGen complements the above: when there is no
// cache entry, renderMapCmd must dispatch a goroutine and bump the counter.
func TestRenderMapCmdMissBumpsGen(t *testing.T) {
	m := New(80, 24)
	startGen := *m.renderGen

	if cmd := m.renderMapCmd(); cmd == nil {
		t.Fatal("expected non-nil Cmd on cache miss")
	}
	if got := *m.renderGen; got != startGen+1 {
		t.Fatalf("expected renderGen to bump from %d to %d, got %d", startGen, startGen+1, got)
	}
	if !m.inFlight() {
		t.Fatal("expected inFlight true after dispatching a fresh render")
	}
}

// TestOverlayCenteredBoxComposites unit-tests the overlay primitive that
// View() uses to composite the Loading badge on top of the previous image.
// Picture content can have fewer cells than picRows when the source image
// is small (the actual osm.Render path matches the cell rectangle exactly,
// but tests don't), so this exercises the function directly with predictable
// inputs.
func TestOverlayCenteredBoxComposites(t *testing.T) {
	cols, rows := 40, 10
	row := strings.Repeat("X", cols)
	content := strings.Repeat(row+"\n", rows-1) + row

	overlay := "+---+\n| L |\n+---+"

	got := overlayCenteredBox(content, cols, rows, overlay)
	if !strings.Contains(got, "L") {
		t.Fatalf("expected overlay 'L' to appear in result, got %q", got)
	}
	if !strings.Contains(got, "X") {
		t.Fatal("expected untouched content cells (X) to remain in result")
	}
	if lines := strings.Split(got, "\n"); len(lines) != rows {
		t.Fatalf("expected %d output lines, got %d", rows, len(lines))
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
