package mapview

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	"github.com/charmbracelet/x/ansi"
	sm "github.com/flopp/go-staticmaps"
	"github.com/golang/geo/s2"
)

// RenderMode is an alias for picture.PictureMode so callers can keep using
// mapview.RenderGlyph / mapview.RenderKitty.
type RenderMode = picture.PictureMode

const (
	RenderGlyph = picture.PictureGlyph
	RenderKitty = picture.PictureKitty
)

type Style int8

const (
	Wikimedia Style = iota
	OpenStreetMaps
	OpenTopoMap
	OpenCycleMap
	CartoLight
	CartoDark
	StamenToner
	StamenTerrain
	ArcgisWorldImagery
)

// MapCoordinates is the geocoder-result message a parent forwards into Update
// to recenter the map. Setting Err makes mapview surface it via errMsg / View.
type MapCoordinates struct {
	Lat float64
	Lng float64
	Err error
}

// mapImageMsg carries the image.Image produced by an async tile render.
// Update hands the image to the embedded picture.Model and, on success,
// stores the image in the render cache keyed by the state that produced it.
// The gen field is a monotonic counter set when the render Cmd is
// dispatched; messages whose gen no longer matches the Model's renderGen
// are stale and dropped, so an older render finishing late can't overwrite
// a newer one.
type mapImageMsg struct {
	gen uint64
	key renderKey
	img image.Image
	err error
}

// IsMapUpdate reports whether msg is a message mapview's Update needs to see.
// Parents containing other focusable widgets must forward matching messages
// regardless of focus, otherwise async render results are lost.
func IsMapUpdate(msg tea.Msg) bool {
	switch msg.(type) {
	case MapCoordinates, mapImageMsg:
		return true
	}
	return picture.IsPictureMsg(msg)
}

type NominatimResponse []struct {
	PlaceID     int    `json:"place_id"`
	License     string `json:"license"`
	OSMType     string `json:"osm_type"`
	OSMID       int    `json:"osm_id"`
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	DisplayName string `json:"display_name"`
}

type KeyMap struct {
	Up      key.Binding
	Right   key.Binding
	Down    key.Binding
	Left    key.Binding
	ZoomIn  key.Binding
	ZoomOut key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Right:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Left:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
		ZoomIn:  key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "zoom in")),
		ZoomOut: key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "zoom out")),
	}
}

// Marker is a point to draw on the map. Color and Size fall back to (red, 16).
type Marker struct {
	Lat, Lng float64
	Color    color.Color
	Size     float64
}

// Model is a Bubble Tea model that renders a tile map. Rendering is delegated
// to an embedded picture.Model; mapview owns geo state, key handling, and
// async tile/geocode fetches. Each render builds its own *sm.Context inside
// the goroutine — the Model holds no shared mutable tile context — so rapid
// pan / zoom / resize can safely fire many renders in parallel.
type Model struct {
	KeyMap KeyMap
	Style  lipgloss.Style

	cols, rows int

	initialized bool

	tileProvider *sm.TileProvider
	lat          float64
	lng          float64
	loc          string
	zoom         int
	markers      []Marker
	tileStyle    Style

	// renderGen is bumped every time renderMapCmd dispatches a goroutine.
	// Each in-flight goroutine carries a copy in its mapImageMsg; Update
	// drops messages whose gen no longer matches, so an older render
	// finishing late cannot clobber a newer one.
	//
	// lastAccepted records the gen of the most recently applied
	// mapImageMsg. (renderGen != lastAccepted) means a render is in flight,
	// which the View uses to overlay a "Loading…" badge on the previous
	// image instead of letting the surrounding box collapse.
	//
	// Both are pointers so the counters survive Bubble Tea's value-receiver
	// idiom: methods like Init() return Cmds via a copy of Model, but the
	// shared *uint64 keeps every copy in sync with the live counter.
	renderGen    *uint64
	lastAccepted *uint64

	// cache is an LRU of (state → composited image) so revisiting a known
	// state (e.g. selecting a place that was viewed earlier) applies the
	// previous image synchronously, avoiding the in-flight Loading overlay.
	// Pointer for the same value-receiver-survival reason as the gen
	// counters above. nil when caching is disabled (Config.CacheCap < 0).
	cache    *renderCache
	cacheCap int // 0 means default; <0 disables; >0 sets the LRU size

	// oversample is the linear pixel-density multiplier (always a power
	// of 2 ≥ 1 after normalization in NewWithConfig).
	oversample int

	// opticalZoom magnifies the cached source image at display time.
	// 0 = no zoom. N > 0 means crop center 1/2^N of each axis and let
	// picture.Model scale it back up to the cell rectangle.
	opticalZoom int

	// sourceImage caches the most recent un-cropped image returned by a
	// successful render or cache hit. SetOpticalZoom re-crops this on
	// the fly so changing the zoom factor doesn't require a fresh
	// network/tile fetch.
	sourceImage image.Image

	pic    picture.Model
	errMsg string
}

// Config configures a Model at construction.
type Config struct {
	// Cols and Rows are the initial cell-rectangle dimensions. Equivalent
	// to calling New(cols, rows). Leaving them zero is fine — most
	// consumers learn the real size from the first tea.WindowSizeMsg and
	// hand it to SetSize anyway.
	Cols, Rows int

	// CacheCap tunes the LRU of composited tile images. Cache hits apply
	// the previous image synchronously, so revisiting a state doesn't
	// flash the Loading overlay. Each entry is roughly
	// (cols × osmPxPerCellW × Oversample) × (rows × osmPxPerCellH × Oversample)
	// RGBA pixels — about 940 KB at 80×24 with the default oversample.
	//
	// Zero means the default (defaultRenderCacheCap = 16). Negative
	// disables caching entirely — every render goes through the goroutine
	// path and every state change shows the Loading overlay.
	CacheCap int

	// Oversample is an optical-zoom factor for the source-image canvas.
	// A value of N (1, 2, 4, …) multiplies the per-cell pixel resolution
	// by N AND bumps the OSM tile zoom level by log2(N), keeping the
	// geographic area unchanged while giving the Kitty terminal N×
	// more pixels per cell to sample from. Rough trade-off:
	//
	//	1 (default) → 8×16 px/cell, no extra tiles, current behavior
	//	2          → 16×32 px/cell, ~4× more tiles, noticeably sharper
	//	4          → 32×64 px/cell, ~16× more tiles, hi-DPI quality
	//
	// Non-power-of-2 values are floored to the nearest power of 2.
	// At max zoom (19) the boost is reduced so the effective tile zoom
	// stays valid; the px scale is reduced to match. Glyph mode pays
	// the tile cost without visible benefit (ansimage downscales to
	// half-block resolution either way).
	Oversample int

	// OpticalZoom magnifies the cached source image without fetching
	// new tiles — the renderer crops the center 1/2^N of each axis and
	// nearest-neighbor upscales it back to the source's original
	// dimensions before handing it to picture.Model. The same-dim
	// upscale matters: integer division of the crop rectangle would
	// otherwise drift the aspect ratio away from the cell rectangle at
	// high N, and ansimage's fit-mode would letterbox the result —
	// visibly shrinking the rendered map box. Output is pixelated
	// (digital zoom) but the cell rectangle stays the same shape.
	// Switching OpticalZoom is purely a CPU operation on the
	// already-rendered source.
	//
	//	0 (default) → no magnification
	//	1          → 2× (center half each way, ¼ of the source area)
	//	2          → 4× (center quarter each way, 1/16 of the area)
	//	3          → 8× …
	//
	// Combine with Oversample for "optical zoom over a higher-resolution
	// source": e.g. Oversample=2 + OpticalZoom=1 gives a 2× zoomed view
	// that's still as sharp as the un-zoomed default.
	OpticalZoom int
}

// New returns a Model sized to cols × rows terminal cells with default
// configuration. Use NewWithConfig to tune cache capacity and other knobs.
func New(cols, rows int) Model {
	return NewWithConfig(Config{Cols: cols, Rows: rows})
}

// NewWithConfig returns a Model with the supplied Config. Zero fields are
// filled with defaults; negative CacheCap disables caching. Oversample is
// floored to the nearest power of 2 ≥ 1; OpticalZoom is clamped to ≥ 0.
func NewWithConfig(cfg Config) Model {
	oz := cfg.OpticalZoom
	if oz < 0 {
		oz = 0
	}
	m := Model{
		cols:        cfg.Cols,
		rows:        cfg.Rows,
		cacheCap:    cfg.CacheCap,
		oversample:  floorPow2(cfg.Oversample),
		opticalZoom: oz,
	}
	m.setInitialValues()
	return m
}

// floorPow2 returns the largest power of 2 ≤ n, with a floor of 1. So
// floorPow2(0) == floorPow2(1) == 1; floorPow2(3) == 2; floorPow2(7) == 4.
func floorPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p<<1 <= n {
		p <<= 1
	}
	return p
}

// log2 returns ⌊log₂(n)⌋ for n ≥ 1. Caller must ensure n is a power of 2
// for an exact result; otherwise it returns the floor.
func log2(n int) int {
	if n <= 1 {
		return 0
	}
	r := 0
	for n > 1 {
		n >>= 1
		r++
	}
	return r
}

func (m *Model) setInitialValues() {
	m.KeyMap = DefaultKeyMap()
	m.tileProvider = sm.NewTileProviderOpenStreetMaps()
	m.tileStyle = OpenStreetMaps
	m.zoom = 15
	m.lat = 25.0782266
	m.lng = -77.3383438
	m.loc = ""
	m.pic = picture.New()
	m.pic.SetSize(m.cols, m.picRows())
	if m.renderGen == nil {
		var g uint64
		m.renderGen = &g
	}
	if m.lastAccepted == nil {
		var a uint64
		m.lastAccepted = &a
	}
	if m.cacheCap == 0 {
		m.cacheCap = defaultRenderCacheCap
	}
	if m.cache == nil && m.cacheCap > 0 {
		m.cache = newRenderCache(m.cacheCap)
	}
	if m.oversample < 1 {
		m.oversample = 1
	}
	m.initialized = true
}

// OpticalZoom returns the current optical-zoom level. 0 means no
// magnification; N > 0 means a 2^N center crop of the source image.
func (m Model) OpticalZoom() int { return m.opticalZoom }

// SetOpticalZoom adjusts the digital magnification of the cached source
// image. n is clamped to >= 0. If a source image is already on hand, the
// result is applied synchronously (no network or tile-render goroutine);
// otherwise the next renderMapCmd will pick up the new value.
func (m *Model) SetOpticalZoom(n int) tea.Cmd {
	if n < 0 {
		n = 0
	}
	if n == m.opticalZoom {
		return nil
	}
	m.opticalZoom = n
	if m.sourceImage == nil {
		return m.renderMapCmd()
	}
	return m.pic.SetImage(opticalCrop(m.sourceImage, opticalCropFactor(n)))
}

// opticalCropFactor returns the linear divisor for a given OpticalZoom
// level. Level 0 → 1 (no crop), 1 → 2 (center half), 2 → 4 (center
// quarter), etc.
func opticalCropFactor(opticalZoom int) int {
	if opticalZoom <= 0 {
		return 1
	}
	return 1 << opticalZoom
}

// opticalCrop takes the center 1/factor portion of img on each axis, then
// nearest-neighbor upscales it back to img's original dimensions. Two
// invariants matter for the rendered map to look right:
//
//  1. Output dims == source dims. picture.Model's fit-mode would
//     otherwise letterbox the result, visibly shrinking the cell
//     rectangle as the zoom climbs.
//  2. (w - cw) and (h - ch) are even. The crop is centered around
//     (b.Min + (w-cw)/2, …); when that margin is odd the crop sits half
//     a source pixel off-center. After upscaling by `factor`, that
//     half-pixel becomes `factor/2` source pixels of shift — at
//     factor=16 (OpticalZoom=4) on a 368-px-tall source, that's a full
//     cell of vertical drift. Snapping cw and ch down by 1 when needed
//     keeps the center exact at every zoom level.
func opticalCrop(img image.Image, factor int) image.Image {
	if img == nil || factor <= 1 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cw, ch := w/factor, h/factor
	// Ensure (w - cw) and (h - ch) are even so the crop is exactly
	// centered on (w/2, h/2).
	cw -= (w - cw) & 1
	ch -= (h - ch) & 1
	if cw < 1 || ch < 1 {
		return img
	}
	x0 := b.Min.X + (w-cw)/2
	y0 := b.Min.Y + (h-ch)/2

	// Nearest-neighbor upscale of the cropped rect back to (w × h).
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := y0 + y*ch/h
		for x := 0; x < w; x++ {
			sx := x0 + x*cw/w
			out.Set(x, y, img.At(sx, sy))
		}
	}
	return out
}

// effectiveOversample returns the (oversample factor, OSM zoom boost)
// actually used for the next render. Both are derived from m.oversample,
// then capped so the resulting tile zoom (m.zoom + boost) doesn't exceed
// maxOSMZoom. At the cap a request like "zoom 18, oversample 4" silently
// degrades to "tile zoom 19, oversample 2" — same area, less sharp than
// requested but never an invalid OSM request.
func (m Model) effectiveOversample() (factor, boost int) {
	factor = m.oversample
	if factor < 1 {
		factor = 1
	}
	boost = log2(factor)
	for m.zoom+boost > maxOSMZoom && factor > 1 {
		factor >>= 1
		boost--
	}
	return factor, boost
}

// inFlight reports whether at least one render has been dispatched whose
// result hasn't been applied yet — i.e. the displayed picture is older than
// the latest geo state.
func (m Model) inFlight() bool {
	if m.renderGen == nil || m.lastAccepted == nil {
		return false
	}
	return *m.renderGen != *m.lastAccepted
}

// Center returns the current map center.
func (m Model) Center() (lat, lng float64) { return m.lat, m.lng }

// Zoom returns the current zoom level.
func (m Model) Zoom() int { return m.zoom }

// Pixels-per-terminal-cell used to size the underlying tile-render canvas.
// The 1:2 ratio matches the half-block cell aspect, so ansimage's fit-mode
// has no slack to leave behind and the map fills the entire enclosure.
// Config.Oversample multiplies these for sharper Kitty renders without
// changing the visible geographic area (see the optical-zoom note there).
const (
	osmPxPerCellW = 8
	osmPxPerCellH = 16

	// maxOSMZoom caps the OSM tile zoom we'll request, including any
	// oversample boost. Most providers serve up to z=19; going higher
	// returns 404s.
	maxOSMZoom = 19
)

// AttributionText is the OSM credit pinned to the bottom row of the rendered
// map. OpenStreetMap's attribution policy requires it on every visible map.
const AttributionText = "Maps and Data (c) openstreetmap.org and contributors"

// attributionMinRows is the smallest cell-rectangle height that still leaves
// at least one row for the actual map after reserving the attribution row.
const attributionMinRows = 2

// defaultRenderCacheCap is the number of composited tile-images cached
// keyed on (lat, lng, zoom, cols, rows, style, markers). A cache hit lets
// SetLatLng / SetStyle / etc. apply the prior image synchronously, skipping
// the goroutine roundtrip and the in-flight Loading overlay. Each entry is
// a (cols × osmPxPerCellW) × (rows × osmPxPerCellH) RGBA image — for an
// 80×24 cell viewport that's ~940 KB.
const defaultRenderCacheCap = 16

// renderKey identifies a fully-rendered tile image. All fields participate
// in the equality comparison, including the serialized marker list, so two
// states that produce visually identical output share a cache entry.
// oversample is the EFFECTIVE multiplier (after maxZoom capping), not the
// requested one — two requests that produce the same output share a slot.
type renderKey struct {
	lat, lng   float64
	zoom       int
	cols       int
	rows       int
	style      Style
	oversample int
	markers    string
}

func makeRenderKey(lat, lng float64, zoom, cols, rows int, style Style, oversample int, markers []Marker) renderKey {
	var sb strings.Builder
	for _, mk := range markers {
		fmt.Fprintf(&sb, "%g,%g,%g,%v;", mk.Lat, mk.Lng, mk.Size, mk.Color)
	}
	return renderKey{
		lat:        lat,
		lng:        lng,
		zoom:       zoom,
		cols:       cols,
		rows:       rows,
		style:      style,
		oversample: oversample,
		markers:    sb.String(),
	}
}

// renderCache is a small LRU of composited tile images. Access is single-
// goroutine (Update + the synchronous render-Cmd construction in
// renderMapCmd both run on Bubble Tea's main goroutine), so it doesn't
// need locking.
type renderCache struct {
	cap     int
	entries map[renderKey]image.Image
	order   []renderKey
}

func newRenderCache(cap int) *renderCache {
	return &renderCache{cap: cap, entries: make(map[renderKey]image.Image)}
}

func (c *renderCache) get(k renderKey) (image.Image, bool) {
	img, ok := c.entries[k]
	if ok {
		c.markUsed(k)
	}
	return img, ok
}

func (c *renderCache) put(k renderKey, img image.Image) {
	if _, ok := c.entries[k]; ok {
		c.markUsed(k)
		c.entries[k] = img
		return
	}
	c.entries[k] = img
	c.order = append(c.order, k)
	for len(c.order) > c.cap {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, evict)
	}
}

func (c *renderCache) markUsed(k renderKey) {
	for i, e := range c.order {
		if e == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, k)
			return
		}
	}
}

// picRows returns the rows available to the embedded picture.Model after
// reserving space for the attribution strip.
func (m Model) picRows() int {
	if m.rows >= attributionMinRows {
		return m.rows - 1
	}
	return m.rows
}

// SetSize updates render dimensions in terminal cells. Returns a Cmd that
// re-syncs picture.Model and re-renders the map. The tile canvas is sized
// per-render inside renderMapCmd to match the cell-rectangle aspect ratio,
// so the rendered image flows the entire enclosure rather than letterboxing.
// One row is reserved at the bottom for the OSM attribution strip.
func (m *Model) SetSize(cols, rows int) tea.Cmd {
	if cols == m.cols && rows == m.rows {
		return nil
	}
	m.cols = cols
	m.rows = rows
	picCmd := m.pic.SetSize(cols, m.picRows())
	return tea.Batch(picCmd, m.renderMapCmd())
}

// SetMarkers replaces all currently-drawn markers.
func (m *Model) SetMarkers(markers []Marker) {
	m.markers = markers
}

// ClearMarkers removes all markers from the map.
func (m *Model) ClearMarkers() {
	m.markers = nil
}

func (m *Model) SetLatLng(lat, lng float64, zoom int) {
	m.lat = lat
	m.lng = lng
	m.zoom = zoom
}

func (m *Model) SetLocation(loc string, zoom int) {
	m.loc = loc
	m.zoom = zoom
}

// RenderMode returns the embedded picture.Model's mode.
func (m Model) RenderMode() RenderMode { return m.pic.Mode() }

// SetRenderMode forwards to picture.Model.Toggle when needed and re-renders.
func (m *Model) SetRenderMode(mode RenderMode) tea.Cmd {
	var cmds []tea.Cmd
	if m.pic.Mode() != mode {
		cmds = append(cmds, m.pic.Toggle())
	}
	cmds = append(cmds, m.renderMapCmd())
	return tea.Batch(cmds...)
}

// TileStyle returns the currently-selected tile style.
func (m Model) TileStyle() Style { return m.tileStyle }

// SetStyle switches the tile provider and re-renders.
func (m *Model) SetStyle(style Style) tea.Cmd {
	switch style {
	case Wikimedia:
		m.tileProvider = sm.NewTileProviderWikimedia()
	case OpenStreetMaps:
		m.tileProvider = sm.NewTileProviderOpenStreetMaps()
	case OpenTopoMap:
		m.tileProvider = sm.NewTileProviderOpenTopoMap()
	case OpenCycleMap:
		m.tileProvider = sm.NewTileProviderOpenCycleMap()
	case CartoLight:
		m.tileProvider = sm.NewTileProviderCartoLight()
	case CartoDark:
		m.tileProvider = sm.NewTileProviderCartoDark()
	case StamenToner:
		m.tileProvider = sm.NewTileProviderStamenToner()
	case StamenTerrain:
		m.tileProvider = sm.NewTileProviderStamenTerrain()
	case ArcgisWorldImagery:
		m.tileProvider = sm.NewTileProviderArcgisWorldImagery()
	}
	m.tileStyle = style
	return m.renderMapCmd()
}

func (m Model) Init() tea.Cmd { return m.renderMapCmd() }

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.initialized {
		m.setInitialValues()
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		var hit bool
		movement := (1000 / math.Pow(2, float64(m.zoom))) / 3

		switch {
		case key.Matches(msg, m.KeyMap.Up):
			m.lat += movement
			if m.lat > 90.0 {
				m.lat = -90.0
			}
			hit = true
		case key.Matches(msg, m.KeyMap.Right):
			m.lng += movement
			if m.lng > 180.0 {
				m.lng = -180.0
			}
			hit = true
		case key.Matches(msg, m.KeyMap.Down):
			m.lat -= movement
			if m.lat < -90.0 {
				m.lat = 90.0
			}
			hit = true
		case key.Matches(msg, m.KeyMap.Left):
			m.lng -= movement
			if m.lng < -180.0 {
				m.lng = 180.0
			}
			hit = true
		case key.Matches(msg, m.KeyMap.ZoomIn):
			if m.zoom < 16 {
				m.zoom += 1
			}
			hit = true
		case key.Matches(msg, m.KeyMap.ZoomOut):
			if m.zoom > 2 {
				m.zoom -= 1
			}
			hit = true
		}
		if hit {
			return m, m.renderMapCmd()
		}
		return m, nil

	case mapImageMsg:
		// Drop stale renders: only the most-recently-dispatched generation
		// is allowed to update picture.Model. Without this guard, a slow
		// goroutine finishing after a faster newer one would replace the
		// fresh image with a stale one.
		if m.renderGen == nil || msg.gen != *m.renderGen {
			return m, nil
		}
		*m.lastAccepted = msg.gen
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.sourceImage = nil
			return m, m.pic.SetImage(nil)
		}
		m.errMsg = ""
		// Cache and remember the un-cropped source so a later
		// SetOpticalZoom can re-crop without going back to the network.
		if m.cache != nil && msg.img != nil {
			m.cache.put(msg.key, msg.img)
		}
		m.sourceImage = msg.img
		return m, m.pic.SetImage(opticalCrop(msg.img, opticalCropFactor(m.opticalZoom)))

	case MapCoordinates:
		m.loc = ""
		if msg.Err != nil {
			m.errMsg = msg.Err.Error()
			return m, m.pic.SetImage(nil)
		}
		m.errMsg = ""
		m.lat = msg.Lat
		m.lng = msg.Lng
		return m, m.renderMapCmd()
	}

	// Unknown messages: forward to picture.Model so KittyFrameMsg gets handled.
	cmd := m.pic.Update(msg)

	// One-shot lookup if SetLocation was called and we haven't dispatched it.
	if m.initialized && m.loc != "" {
		return m, tea.Batch(cmd, m.lookup(m.loc))
	}
	return m, cmd
}

// renderMapCmd produces a Cmd that delivers the composited tile image for
// the Model's current geo state. On a cache hit the image is applied
// synchronously via picture.SetImage, so no gen bump and no in-flight
// state — the View won't show a Loading overlay and the map updates in
// the same Bubble Tea iteration. On a cache miss it snapshots the state
// into a closure and dispatches a goroutine that builds its own fresh
// *sm.Context (sm.Context is not goroutine-safe) to do the tile fetch
// and composite. The returned mapImageMsg carries the generation counter
// used by Update to drop stale results, and the renderKey used to populate
// the cache once the result is accepted.
func (m *Model) renderMapCmd() tea.Cmd {
	if m.cols <= 0 || m.tileProvider == nil {
		return nil
	}
	picRows := m.picRows()
	if picRows <= 0 {
		return nil
	}

	if m.renderGen == nil {
		var g uint64
		m.renderGen = &g
	}
	if m.lastAccepted == nil {
		var a uint64
		m.lastAccepted = &a
	}

	// Optical-zoom math: oversample N (a power of 2) means N× per-cell
	// pixel density at zoom + log2(N), preserving geographic coverage.
	// effectiveOversample handles the maxOSMZoom cap.
	effectiveOversample, zoomBoost := m.effectiveOversample()

	markers := append([]Marker(nil), m.markers...)
	key := makeRenderKey(m.lat, m.lng, m.zoom, m.cols, picRows, m.tileStyle, effectiveOversample, markers)

	if m.cache != nil {
		if cached, ok := m.cache.get(key); ok {
			// Synchronous hit: SetImage now, no goroutine, no gen bump,
			// no in-flight state — so View won't flash the Loading
			// overlay. Cache holds the un-cropped source so optical
			// zoom can still be re-applied without a re-fetch.
			m.sourceImage = cached
			return m.pic.SetImage(opticalCrop(cached, opticalCropFactor(m.opticalZoom)))
		}
	}

	*m.renderGen++
	gen := *m.renderGen

	provider := m.tileProvider
	lat, lng := m.lat, m.lng
	tileZoom := m.zoom + zoomBoost
	pxW := m.cols * osmPxPerCellW * effectiveOversample
	pxH := picRows * osmPxPerCellH * effectiveOversample

	return func() tea.Msg {
		ctx := sm.NewContext()
		ctx.SetTileProvider(provider)
		ctx.SetCenter(s2.LatLngFromDegrees(lat, lng))
		ctx.SetZoom(tileZoom)
		ctx.SetSize(pxW, pxH)
		for _, mk := range markers {
			col := mk.Color
			if col == nil {
				col = color.RGBA{0xff, 0x00, 0x00, 0xff}
			}
			size := mk.Size
			if size == 0 {
				size = 16
			}
			ctx.AddObject(sm.NewMarker(s2.LatLngFromDegrees(mk.Lat, mk.Lng), col, size))
		}
		img, err := ctx.Render()
		return mapImageMsg{gen: gen, key: key, img: img, err: err}
	}
}

func (m *Model) lookup(address string) tea.Cmd {
	return func() tea.Msg {
		u := fmt.Sprintf(
			"https://nominatim.openstreetmap.org/search?q=%s&format=json&polygon=1&addressdetails=1",
			url.QueryEscape(address),
		)

		resp, err := http.Get(u)
		if err != nil {
			return MapCoordinates{Err: err}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return MapCoordinates{Err: err}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return MapCoordinates{Err: errors.New(string(body))}
		}

		var data NominatimResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return MapCoordinates{Err: err}
		}
		if len(data) == 0 {
			return MapCoordinates{Err: errors.New("location not found")}
		}

		lat, err := strconv.ParseFloat(data[0].Lat, 64)
		if err != nil {
			return MapCoordinates{Err: err}
		}
		lon, err := strconv.ParseFloat(data[0].Lon, 64)
		if err != nil {
			return MapCoordinates{Err: err}
		}
		return MapCoordinates{Lat: lat, Lng: lon}
	}
}

func (m Model) View() tea.View {
	if m.errMsg != "" {
		return tea.NewView(m.errMsg)
	}
	if m.cols <= 0 || m.rows <= 0 {
		return tea.NewView("")
	}

	picRows := m.picRows()
	pv := m.pic.View()

	// Body composition rules:
	// - No image yet → fill the cell rectangle with a centered "Loading…"
	//   so the enclosure keeps its full breadth (no collapse).
	// - Image present and a fresher render is in flight → composite a
	//   small "Loading…" badge over the previous image so the user sees
	//   the old map remain while the new one is fetching.
	// - Otherwise → just the picture content.
	body := pv.Content
	switch {
	case body == "":
		body = lipgloss.Place(m.cols, picRows, lipgloss.Center, lipgloss.Center, "Loading…")
	case m.inFlight():
		body = overlayCenteredBox(body, m.cols, picRows, loadingBadge())
	}

	// picture's Glyph mode runs ansimage in ScaleModeFit. Right after a
	// resize that changes the cell rectangle's aspect ratio, the OLD
	// cached source image (still held by picture until the next
	// mapImageMsg lands) has the wrong AR for the new target — fit-mode
	// then saturates the smaller axis and emits fewer than picRows rows.
	// Pad to picRows so the surrounding box's height stays in sync with
	// its sibling columns. Once the new render lands, the body grows
	// back to the natural picRows count and the pad is a no-op.
	body = padLinesBottom(body, picRows)

	// When the height is too small to spare a row, drop the attribution
	// strip rather than starving the map further.
	if picRows == m.rows {
		return tea.NewView(body)
	}

	attribution := lipgloss.NewStyle().
		Width(m.cols).
		Align(lipgloss.Center).
		Foreground(lipgloss.Color("242")).
		Render(truncateForWidth(AttributionText, m.cols))

	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, attribution))
}

// loadingBadge returns a small bordered "Loading…" box used as the
// composited indicator when a fresher render is in flight.
func loadingBadge() string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Foreground(lipgloss.Color("11")).
		Padding(0, 1).
		Render("Loading…")
}

// overlayCenteredBox composites overlay onto the cols × rows cell rectangle
// of content, centered. content is expected to be exactly cols cells wide
// per row. If overlay doesn't fit, content is returned unchanged.
func overlayCenteredBox(content string, cols, rows int, overlay string) string {
	overlayLines := strings.Split(overlay, "\n")
	overlayH := len(overlayLines)
	overlayW := 0
	for _, l := range overlayLines {
		if w := lipgloss.Width(l); w > overlayW {
			overlayW = w
		}
	}
	if overlayW <= 0 || overlayH <= 0 || overlayW > cols || overlayH > rows {
		return content
	}

	x := (cols - overlayW) / 2
	y := (rows - overlayH) / 2

	lines := strings.Split(content, "\n")
	for i, ol := range overlayLines {
		ly := y + i
		if ly < 0 || ly >= len(lines) {
			continue
		}
		left := ansi.Cut(lines[ly], 0, x)
		right := ansi.Cut(lines[ly], x+overlayW, cols)
		lines[ly] = left + ol + right
	}
	return strings.Join(lines, "\n")
}

// padLinesBottom appends blank lines to s until it has at least n lines.
// Used to backfill the body when the underlying renderer returns fewer
// rows than the cell rectangle expects (typically right after a resize
// that changes the cell-rect aspect ratio while picture still holds an
// image rendered for the previous AR).
func padLinesBottom(s string, n int) string {
	if n <= 0 {
		return s
	}
	have := 1 + strings.Count(s, "\n")
	if have >= n {
		return s
	}
	return s + strings.Repeat("\n", n-have)
}

// truncateForWidth shrinks s with an ellipsis so it fits in width terminal
// cells. Returns "" if width is non-positive. Suitable for ASCII captions;
// rune-aware enough for the © symbol.
func truncateForWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}
