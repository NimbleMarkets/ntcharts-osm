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

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
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
	ThunderforestLandscape
	ThunderforestOutdoors
	ThunderforestTransport
	ArcgisWorldImagery
)

// MapCoordinates is the geocoder-result message a parent forwards into Update
// to recenter the map. Setting Err makes mapview surface it via errMsg / View.
type MapCoordinates struct {
	Lat float64
	Lng float64
	Err error
}

// mapImageMsg carries the image.Image produced by an async osm.Render().
// Update hands the image to the embedded picture.Model.
type mapImageMsg struct {
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
// async tile/geocode fetches.
type Model struct {
	KeyMap KeyMap
	Style  lipgloss.Style

	cols, rows int

	initialized bool

	osm          *sm.Context
	tileProvider *sm.TileProvider
	lat          float64
	lng          float64
	loc          string
	zoom         int
	markers      []Marker
	tileStyle    Style

	pic    picture.Model
	errMsg string
}

// New returns a Model sized to cols × rows terminal cells.
func New(cols, rows int) Model {
	m := Model{cols: cols, rows: rows}
	m.setInitialValues()
	return m
}

func (m *Model) setInitialValues() {
	m.KeyMap = DefaultKeyMap()
	m.osm = sm.NewContext()
	m.osm.SetSize(400, 400)
	m.tileProvider = sm.NewTileProviderOpenStreetMaps()
	m.tileStyle = OpenStreetMaps
	m.zoom = 15
	m.lat = 25.0782266
	m.lng = -77.3383438
	m.loc = ""
	m.pic = picture.New()
	m.pic.SetSize(m.cols, m.rows)
	m.applyToOSM()
	m.applyMarkersToOSM()
	m.initialized = true
}

func (m *Model) applyToOSM() {
	m.osm.SetTileProvider(m.tileProvider)
	m.osm.SetCenter(s2.LatLngFromDegrees(m.lat, m.lng))
	m.osm.SetZoom(m.zoom)
}

// Center returns the current map center.
func (m Model) Center() (lat, lng float64) { return m.lat, m.lng }

// Zoom returns the current zoom level.
func (m Model) Zoom() int { return m.zoom }

// SetSize updates render dimensions in terminal cells. Returns a Cmd that
// re-syncs picture.Model and re-renders the map.
func (m *Model) SetSize(cols, rows int) tea.Cmd {
	if cols == m.cols && rows == m.rows {
		return nil
	}
	m.cols = cols
	m.rows = rows
	picCmd := m.pic.SetSize(cols, rows)
	return tea.Batch(picCmd, m.renderMapCmd())
}

// SetMarkers replaces all currently-drawn markers.
func (m *Model) SetMarkers(markers []Marker) {
	m.markers = markers
	m.applyMarkersToOSM()
}

// ClearMarkers removes all markers from the map.
func (m *Model) ClearMarkers() {
	m.markers = nil
	if m.osm != nil {
		m.osm.ClearObjects()
	}
}

func (m *Model) applyMarkersToOSM() {
	if m.osm == nil {
		return
	}
	m.osm.ClearObjects()
	for _, mk := range m.markers {
		col := mk.Color
		if col == nil {
			col = color.RGBA{0xff, 0x00, 0x00, 0xff}
		}
		size := mk.Size
		if size == 0 {
			size = 16
		}
		m.osm.AddObject(sm.NewMarker(s2.LatLngFromDegrees(mk.Lat, mk.Lng), col, size))
	}
}

func (m *Model) SetLatLng(lat, lng float64, zoom int) {
	m.lat = lat
	m.lng = lng
	m.zoom = zoom
	m.applyToOSM()
}

func (m *Model) SetLocation(loc string, zoom int) {
	m.loc = loc
	m.zoom = zoom
	m.applyToOSM()
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
	case ThunderforestLandscape:
		m.tileProvider = sm.NewTileProviderThunderforestLandscape(getThunderforestAPIKey())
	case ThunderforestOutdoors:
		m.tileProvider = sm.NewTileProviderThunderforestOutdoors(getThunderforestAPIKey())
	case ThunderforestTransport:
		m.tileProvider = sm.NewTileProviderThunderforestTransport(getThunderforestAPIKey())
	case ArcgisWorldImagery:
		m.tileProvider = sm.NewTileProviderArcgisWorldImagery()
	}
	m.tileStyle = style
	m.applyToOSM()
	return m.renderMapCmd()
}

func getThunderforestAPIKey() string { return "YOUR_THUNDERFOREST_API_KEY" }

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
			m.applyToOSM()
			return m, m.renderMapCmd()
		}
		return m, nil

	case mapImageMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, m.pic.SetImage(nil)
		}
		m.errMsg = ""
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
		m.applyToOSM()
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

// renderMapCmd dispatches the (synchronous) tile fetch + composite work to a
// goroutine and returns a Cmd that produces a mapImageMsg.
func (m *Model) renderMapCmd() tea.Cmd {
	if m.cols <= 0 || m.rows <= 0 {
		return nil
	}
	osm := m.osm
	return func() tea.Msg {
		img, err := osm.Render()
		return mapImageMsg{img: img, err: err}
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
	return m.pic.View()
}
