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

// TestCacheHitDuringStaleInFlight pins the fix for the resize/loading race:
// when a goroutine is in flight at gen=N and the next renderMapCmd resolves
// via the cache (e.g. user pans/resizes back to a recently-visited spot),
// the cache-hit branch must invalidate the in-flight gen so its result
// can't overwrite the just-applied cached image.
func TestCacheHitDuringStaleInFlight(t *testing.T) {
	m := New(80, 24)

	// Dispatch a render so we have a stale gen "in flight".
	if cmd := m.renderMapCmd(); cmd == nil {
		t.Fatal("expected initial renderMapCmd to return a Cmd")
	}
	staleGen := *m.renderGen
	if !m.inFlight() {
		t.Fatal("expected inFlight true after first dispatch")
	}

	// Pre-populate the cache so the next renderMapCmd takes the synchronous hit.
	cachedImg := newSolidImage(color.RGBA{R: 1, G: 2, B: 3, A: 255})
	key := makeRenderKey(m.lat, m.lng, m.zoom, m.cols, m.picRows(), m.tileStyle, m.oversample, m.maxAspectRatio, m.letterboxColor, m.markers)
	m.cache.put(key, cachedImg)

	// Cache-hit transition.
	_ = m.renderMapCmd()
	if *m.renderGen == staleGen {
		t.Fatal("expected cache hit to bump renderGen so stale in-flight result is invalidated")
	}
	if m.inFlight() {
		t.Fatal("cache hit must leave inFlight false")
	}

	// Stale goroutine returns: must be dropped, leaving the cached source untouched.
	staleImg := newSolidImage(color.RGBA{R: 250, G: 0, B: 0, A: 255})
	updated, _ := m.Update(mapImageMsg{gen: staleGen, img: staleImg, key: key})
	if updated.sourceImage != cachedImg {
		t.Fatal("stale in-flight result must not overwrite the cache-hit sourceImage")
	}
}

// TestRenderMapCmdHitsCacheSynchronously verifies that a renderKey already
// present in the cache short-circuits renderMapCmd: SetImage is called
// synchronously, no goroutine is dispatched, and inFlight stays false so
// View doesn't flash the Loading overlay. renderGen is bumped (and
// lastAccepted brought along with it) so any older in-flight render's
// mapImageMsg is dropped as stale when it lands.
func TestRenderMapCmdHitsCacheSynchronously(t *testing.T) {
	m := New(80, 24)

	// Pre-populate the cache with the entry that the current state would
	// look up.
	cachedImg := newSolidImage(color.RGBA{R: 1, G: 2, B: 3, A: 255})
	key := makeRenderKey(m.lat, m.lng, m.zoom, m.cols, m.picRows(), m.tileStyle, m.oversample, m.maxAspectRatio, m.letterboxColor, m.markers)
	m.cache.put(key, cachedImg)

	startGen := *m.renderGen

	// In glyph mode pic.SetImage returns nil (no Kitty frame to schedule),
	// so we don't assert on the Cmd's nil-ness — only on the gen bump and
	// the absence of in-flight bookkeeping that would trigger the Loading
	// overlay.
	_ = m.renderMapCmd()
	if got := *m.renderGen; got != startGen+1 {
		t.Fatalf("expected cache hit to bump renderGen by 1 (was %d, got %d)", startGen, got)
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
	k1 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, 0, nil, nil)
	k2 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 2, 0, nil, nil)
	if k1 == k2 {
		t.Fatal("oversample 1 and 2 must produce different cache keys")
	}
}

// TestOpticalCrop_Geometry verifies the geometry invariants downstream
// rendering depends on:
//  1. Same bounds as the source.
//  2. Output (0,0) reads the crop top-left, not a corner of the source —
//     proves the crop region is centered, not anchored.
//  3. Output center reads the source center (no centering drift). Uses
//     a 64×64 source so (w-cw) and (h-ch) are both even at factor 4.
func TestOpticalCrop_Geometry(t *testing.T) {
	const w, h = 64, 64
	const factor = 4 // cw=16, ch=16, x0=24, y0=24

	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	src.SetRGBA(24, 24, color.RGBA{R: 1, A: 255})       // crop top-left
	src.SetRGBA(w-1, 0, color.RGBA{G: 50, A: 255})      // OUTSIDE crop
	src.SetRGBA(w/2, h/2, color.RGBA{B: 1, A: 255})     // source center

	out := opticalCrop(src, factor)

	// (1) Same dims.
	if b := out.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("expected output %d×%d, got %d×%d", w, h, b.Dx(), b.Dy())
	}

	// (2) Output (0,0) reads crop top-left (red marker).
	r, g, b, _ := out.At(0, 0).RGBA()
	if r>>8 != 1 || g>>8 != 0 || b>>8 != 0 {
		t.Errorf("output (0,0) should sample crop top-left (R=1), got R=%d G=%d B=%d", r>>8, g>>8, b>>8)
	}

	// (3) Output center reads source center (blue marker). At this size
	// and factor the crop is exactly centered, so no neighborhood
	// tolerance is needed.
	r, g, b, _ = out.At(w/2, h/2).RGBA()
	if b>>8 != 1 || r>>8 != 0 || g>>8 != 0 {
		t.Errorf("output (%d,%d) should sample source center (B=1), got R=%d G=%d B=%d",
			w/2, h/2, r>>8, g>>8, b>>8)
	}

	// The corner OUTSIDE the crop region should never appear in the
	// output. Walk the whole output and verify no pixel matches the
	// "outside" marker.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			_, g, _, _ := out.At(x, y).RGBA()
			if g>>8 == 50 {
				t.Errorf("output (%d,%d) sampled a pixel from outside the crop region", x, y)
			}
		}
	}
}

// TestOpticalCrop_ParityFixForRealisticSize covers the bug that was
// shifting the rendered map at high optical zoom on the typical mapview
// source size. 640×368 at factor 16 would have ch = 23 with (h-ch) = 345
// (odd), placing the crop half a source pixel off-center vertically. The
// upscale by factor 16 amplified that to a full cell of drift in Kitty
// mode. opticalCrop now snaps cw/ch down by 1 when needed so the margin
// is always even.
func TestOpticalCrop_ParityFixForRealisticSize(t *testing.T) {
	const w, h = 640, 368 // typical: cols=80, picRows=23, osmPxPerCellW=8, osmPxPerCellH=16

	src := image.NewRGBA(image.Rect(0, 0, w, h))
	src.SetRGBA(w/2, h/2, color.RGBA{B: 1, A: 255}) // source center
	// Paint everything else "anything not blue" by leaving zero RGBA.

	for _, factor := range []int{2, 4, 8, 16, 32} {
		out := opticalCrop(src, factor)
		b := out.Bounds()
		if b.Dx() != w || b.Dy() != h {
			t.Errorf("factor %d: expected dims %d×%d, got %d×%d", factor, w, h, b.Dx(), b.Dy())
			continue
		}

		// The source center is the crop center too. After nearest-
		// neighbor upscale, output (w/2, h/2) ± a small neighborhood
		// must contain a blue pixel — this fails if the crop is off-
		// center by more than 1 source-pixel-worth (which gets
		// amplified by factor on upscale).
		foundBlue := false
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				_, _, b, _ := out.At(w/2+dx, h/2+dy).RGBA()
				if b>>8 == 1 {
					foundBlue = true
				}
			}
		}
		if !foundBlue {
			t.Errorf("factor %d: output center neighborhood missed the source-center marker — crop is off-center", factor)
		}
	}
}

// TestTargetMapDims pins the AR-clamping math used by MaxAspectRatio.
// Cell rect AR is clamped into [1/maxAR, maxAR]; outside that range the
// map portion is shrunk to the boundary AR.
func TestTargetMapDims(t *testing.T) {
	cases := []struct {
		name                       string
		cellRectW, cellRectH       int
		maxAR                      float64
		wantMapW, wantMapH         int
	}{
		{"unconstrained 0", 800, 400, 0, 800, 400},
		{"unconstrained negative", 800, 400, -1, 800, 400},
		{"in range, no letterbox", 800, 400, 3.0, 800, 400}, // AR=2.0, in [0.33, 3.0]
		{"too wide, clamp at maxAR", 800, 100, 2.0, 200, 100}, // AR=8.0 > 2.0 → mapW=H*2
		{"too tall, clamp at 1/maxAR", 100, 800, 2.0, 100, 200}, // AR=0.125 < 0.5 → mapH=W*2
		{"square cap, wider rect", 800, 400, 1.0, 400, 400},     // AR=2.0 > 1.0 → square at H
		{"square cap, taller rect", 400, 800, 1.0, 400, 400},    // AR=0.5 < 1.0 → square at W
		{"degenerate width", 0, 400, 2.0, 0, 400},
		{"degenerate height", 800, 0, 2.0, 800, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := targetMapDims(tc.cellRectW, tc.cellRectH, tc.maxAR)
			if gotW != tc.wantMapW || gotH != tc.wantMapH {
				t.Errorf("targetMapDims(%d, %d, %v) = (%d, %d); want (%d, %d)",
					tc.cellRectW, tc.cellRectH, tc.maxAR,
					gotW, gotH, tc.wantMapW, tc.wantMapH)
			}
		})
	}
}

// TestComposeLetterbox verifies the letterbox composition produces a
// cell-rect-sized canvas with the map at center and bg fill outside.
func TestComposeLetterbox(t *testing.T) {
	// Map is a solid green 200×100 image.
	mapImg := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			mapImg.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
		}
	}
	// Compose into 800×400 cell rect with red bg.
	bg := color.RGBA{R: 255, A: 255}
	out := composeLetterbox(mapImg, 800, 400, bg)
	if got := out.Bounds(); got.Dx() != 800 || got.Dy() != 400 {
		t.Fatalf("composed dims: expected 800×400, got %d×%d", got.Dx(), got.Dy())
	}
	// Map should be centered: offset (300, 150) → (500, 250).
	r, g, _, _ := out.At(400, 200).RGBA() // map center
	if g>>8 != 255 || r>>8 != 0 {
		t.Errorf("expected map center pixel green, got R=%d G=%d", r>>8, g>>8)
	}
	r, g, _, _ = out.At(0, 0).RGBA() // top-left corner = letterbox bg
	if r>>8 != 255 || g>>8 != 0 {
		t.Errorf("expected (0,0) to be letterbox bg (red), got R=%d G=%d", r>>8, g>>8)
	}
	r, g, _, _ = out.At(799, 399).RGBA() // bottom-right corner = letterbox bg
	if r>>8 != 255 || g>>8 != 0 {
		t.Errorf("expected (799,399) to be letterbox bg (red), got R=%d G=%d", r>>8, g>>8)
	}
}

// TestComposeLetterbox_NoOpWhenSizesMatch verifies no allocation happens
// when the map already fills the cell rect (the common unconstrained
// case where MaxAspectRatio=0 → mapImg.Bounds() == cell rect).
func TestComposeLetterbox_NoOpWhenSizesMatch(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 400))
	out := composeLetterbox(src, 800, 400, color.RGBA{R: 255, A: 255})
	if image.Image(src) != out {
		t.Error("composeLetterbox should return the input unchanged when dims match")
	}
}

// TestRenderKey_DistinguishesMaxAspectRatio verifies that two requests
// for the same state but different MaxAspectRatio don't collide in the
// cache (different maxAR yields different cached source — letterbox
// bands are baked in).
func TestRenderKey_DistinguishesMaxAspectRatio(t *testing.T) {
	bg := color.RGBA{A: 255}
	k1 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, 0, bg, nil)
	k2 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, 2.0, bg, nil)
	if k1 == k2 {
		t.Fatal("MaxAspectRatio 0 and 2.0 must produce different cache keys")
	}
}

// TestRenderKey_DistinguishesLetterboxColor verifies cache keying on
// letterbox color too — different bg colors mean visually different
// cached sources.
func TestRenderKey_DistinguishesLetterboxColor(t *testing.T) {
	black := color.RGBA{A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	k1 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, 2.0, black, nil)
	k2 := makeRenderKey(0, 0, 10, 80, 23, OpenStreetMaps, 1, 2.0, white, nil)
	if k1 == k2 {
		t.Fatal("different LetterboxColor must produce different cache keys")
	}
}

// TestPadLinesBottom pins the helper that backfills picture content when
// it undershoots the cell rectangle's row count (e.g. when ansimage's
// fit-mode saturates the wrong axis right after a resize). The body must
// always come out exactly n lines tall so the surrounding box matches
// its sibling columns.
func TestPadLinesBottom(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want int // expected line count
	}{
		{"already enough", "a\nb\nc", 3, 3},
		{"more than enough", "a\nb\nc\nd", 3, 4},
		{"short by one", "a\nb", 3, 3},
		{"short by many", "a", 5, 5},
		{"empty padded to n", "", 4, 4},
		{"n=0 leaves untouched", "a\nb", 0, 2},
		{"negative n leaves untouched", "a", -3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := padLinesBottom(tc.in, tc.n)
			gotLines := 0
			if got != "" {
				gotLines = 1 + strings.Count(got, "\n")
			}
			if gotLines != tc.want {
				t.Errorf("padLinesBottom(%q, %d) → %d lines, want %d (got %q)",
					tc.in, tc.n, gotLines, tc.want, got)
			}
		})
	}
}

// TestSetOpticalZoom_KittyEndToEnd exercises the full Kitty render path
// after an optical-zoom change to confirm the renderCmd emits a frame
// with the cell-rectangle dimensions picture knows about and a non-empty
// grid+APC. A buggy crop (e.g. one that shifted bounds or emitted
// wrong-size output) would break this end-to-end.
func TestSetOpticalZoom_KittyEndToEnd(t *testing.T) {
	m := New(80, 24)

	// Toggle into Kitty mode. SetRenderMode bumps renderGen via its
	// renderMapCmd call, so subsequent mapImageMsgs need to use the
	// current gen.
	if cmd := m.SetRenderMode(RenderKitty); cmd == nil {
		t.Fatal("expected non-nil Cmd from SetRenderMode(RenderKitty)")
	}
	if m.RenderMode() != RenderKitty {
		t.Fatalf("expected RenderKitty, got %v", m.RenderMode())
	}

	// Seed a source image. Use the *current* renderGen so Update accepts
	// the message instead of dropping it as stale.
	src := newSolidImage(color.RGBA{R: 100, G: 200, B: 50, A: 255})
	updated, _ := m.Update(mapImageMsg{gen: *m.renderGen, img: src})
	if updated.sourceImage == nil {
		t.Fatal("expected sourceImage to be cached after a successful Update")
	}

	// Optical zoom change: should re-crop the cached source synchronously
	// and feed picture.Model directly — no renderMapCmd dispatch.
	startGen := *updated.renderGen
	cmd := updated.SetOpticalZoom(2)
	if got := *updated.renderGen; got != startGen {
		t.Fatalf("SetOpticalZoom with cached source must NOT bump renderGen, was %d → %d", startGen, got)
	}
	if cmd == nil {
		t.Fatal("SetOpticalZoom should return a Cmd in Kitty mode (picture.SetImage emits a renderCmd)")
	}

	// Drain the Cmd to a KittyFrameMsg and verify it carries a populated
	// grid + APC at the expected cell-rectangle dimensions.
	msg := cmd()
	frame, ok := msg.(picture.KittyFrameMsg)
	if !ok {
		t.Fatalf("expected picture.KittyFrameMsg, got %T", msg)
	}
	if frame.Grid == "" {
		t.Error("expected non-empty kitty grid after optical-zoom Cmd")
	}
	if frame.APC == "" {
		t.Error("expected non-empty kitty APC after optical-zoom Cmd")
	}

	// The grid should be sized for the cell rectangle picture knows about
	// (cols × picRows). Counting newlines is a cheap height check.
	picRows := updated.picRows()
	if got := strings.Count(frame.Grid, "\n") + 1; got != picRows {
		t.Errorf("expected kitty grid to be %d rows tall, got %d", picRows, got)
	}
}

// TestOpticalCrop verifies that opticalCrop preserves source dimensions —
// the cropped portion is upscaled back to the original size. This invariant
// is critical: if it failed, ansimage's fit-mode would letterbox the
// rendered cell rectangle as the zoom climbed, visibly shrinking the map
// box. It also verifies that the crop pulls from the CENTER region (not a
// corner) by using a quadrant-colored source where only the center cluster
// can survive any reasonable crop+upscale.
func TestOpticalCrop(t *testing.T) {
	// 80×60 source, painted red except for an 8×8 green block at the
	// center (36..44, 26..34). The block is large enough that
	// nearest-neighbor sampling will hit several green pixels on
	// upscale even with integer-math edge effects.
	src := image.NewRGBA(image.Rect(0, 0, 80, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	for y := 26; y < 34; y++ {
		for x := 36; x < 44; x++ {
			src.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
		}
	}

	// factor 1 → unchanged (returns the input pointer).
	if got := opticalCrop(src, 1); image.Image(src) != got {
		t.Errorf("factor 1 should return the original input")
	}

	for _, factor := range []int{2, 4, 8} {
		out := opticalCrop(src, factor)
		b := out.Bounds()
		if b.Dx() != 80 || b.Dy() != 60 {
			t.Errorf("factor %d: expected output dims to match source 80×60, got %d×%d",
				factor, b.Dx(), b.Dy())
			continue
		}
		// Count green pixels in the output. With a center-anchored crop
		// and the green block at the source center, every factor's
		// upscale should hit a meaningful number of green output pixels;
		// a buggy crop that pulled from a corner would land in the red
		// region exclusively.
		var green int
		for y := 0; y < 60; y++ {
			for x := 0; x < 80; x++ {
				r, g, _, _ := out.At(x, y).RGBA()
				if g > r {
					green++
				}
			}
		}
		if green == 0 {
			t.Errorf("factor %d: expected some green pixels in upscaled output (crop should hit center region)", factor)
		}
	}

	// Degenerate: factor large enough that crop dims would be < 1.
	// opticalCrop should fall back to the original input.
	tiny := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if got := opticalCrop(tiny, 8); got.Bounds().Dx() == 0 {
		t.Error("factor that would produce zero-width crop should fall back to original")
	}
}

// TestSetOpticalZoom_NoSourceQueuesRender verifies that SetOpticalZoom
// before any tile render dispatches a fresh render Cmd.
func TestSetOpticalZoom_NoSourceQueuesRender(t *testing.T) {
	m := New(80, 24)
	startGen := *m.renderGen
	cmd := m.SetOpticalZoom(2)
	if cmd == nil {
		t.Fatal("expected a Cmd when no source image is cached")
	}
	if got := *m.renderGen; got != startGen+1 {
		t.Fatalf("expected renderMapCmd to bump renderGen, was %d, got %d", startGen, got)
	}
	if m.opticalZoom != 2 {
		t.Errorf("expected opticalZoom = 2, got %d", m.opticalZoom)
	}
}

// TestSetOpticalZoom_WithSourceAppliesSync verifies that once a render
// has produced a source image, changing the zoom doesn't dispatch a new
// render — it just re-crops the cached source.
func TestSetOpticalZoom_WithSourceAppliesSync(t *testing.T) {
	m := New(80, 24)
	src := newSolidImage(color.RGBA{R: 200, A: 255})
	updated, _ := m.Update(mapImageMsg{gen: 0, img: src})
	if updated.sourceImage == nil {
		t.Fatal("expected source image to be remembered after a successful render")
	}

	startGen := *updated.renderGen
	_ = updated.SetOpticalZoom(1) // 2× zoom
	if got := *updated.renderGen; got != startGen {
		t.Fatalf("expected SetOpticalZoom with cached source NOT to bump renderGen (was %d, got %d)", startGen, got)
	}
	if updated.opticalZoom != 1 {
		t.Errorf("expected opticalZoom = 1, got %d", updated.opticalZoom)
	}
}

// TestSetOpticalZoom_NoOpOnSameValue verifies that SetOpticalZoom with the
// current value returns nil and doesn't churn picture.Model.
func TestSetOpticalZoom_NoOpOnSameValue(t *testing.T) {
	m := New(80, 24)
	if cmd := m.SetOpticalZoom(0); cmd != nil {
		t.Fatalf("expected nil Cmd for SetOpticalZoom(0) when current is 0, got %v", cmd)
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
