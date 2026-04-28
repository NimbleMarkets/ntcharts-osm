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
	// (cols × osmPxPerCellW) × (rows × osmPxPerCellH) RGBA pixels — about
	// 940 KB at 80×24.
	//
	// Zero means the default (defaultRenderCacheCap = 16). Negative
	// disables caching entirely — every render goes through the goroutine
	// path and every state change shows the Loading overlay.
	CacheCap int
}

// New returns a Model sized to cols × rows terminal cells with default
// configuration. Use NewWithConfig to tune cache capacity and other knobs.
func New(cols, rows int) Model {
	return NewWithConfig(Config{Cols: cols, Rows: rows})
}

// NewWithConfig returns a Model with the supplied Config. Zero fields are
// filled with defaults; negative CacheCap disables caching.
func NewWithConfig(cfg Config) Model {
	m := Model{cols: cfg.Cols, rows: cfg.Rows, cacheCap: cfg.CacheCap}
	m.setInitialValues()
	return m
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
	m.initialized = true
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
// Higher values = sharper Kitty-mode renders; lower = fewer tiles fetched.
const (
	osmPxPerCellW = 8
	osmPxPerCellH = 16
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
type renderKey struct {
	lat, lng float64
	zoom     int
	cols     int
	rows     int
	style    Style
	markers  string
}

func makeRenderKey(lat, lng float64, zoom, cols, rows int, style Style, markers []Marker) renderKey {
	var sb strings.Builder
	for _, mk := range markers {
		fmt.Fprintf(&sb, "%g,%g,%g,%v;", mk.Lat, mk.Lng, mk.Size, mk.Color)
	}
	return renderKey{
		lat:     lat,
		lng:     lng,
		zoom:    zoom,
		cols:    cols,
		rows:    rows,
		style:   style,
		markers: sb.String(),
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
			return m, m.pic.SetImage(nil)
		}
		m.errMsg = ""
		if m.cache != nil && msg.img != nil {
			m.cache.put(msg.key, msg.img)
		}
		return m, m.pic.SetImage(msg.img)

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

	markers := append([]Marker(nil), m.markers...)
	key := makeRenderKey(m.lat, m.lng, m.zoom, m.cols, picRows, m.tileStyle, markers)

	if m.cache != nil {
		if cached, ok := m.cache.get(key); ok {
			// Synchronous hit: SetImage now, no goroutine, no gen bump,
			// no in-flight state — so View won't flash the Loading
			// overlay.
			return m.pic.SetImage(cached)
		}
	}

	*m.renderGen++
	gen := *m.renderGen

	provider := m.tileProvider
	lat, lng, zoom := m.lat, m.lng, m.zoom
	pxW := m.cols * osmPxPerCellW
	pxH := picRows * osmPxPerCellH

	return func() tea.Msg {
		ctx := sm.NewContext()
		ctx.SetTileProvider(provider)
		ctx.SetCenter(s2.LatLngFromDegrees(lat, lng))
		ctx.SetZoom(zoom)
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
