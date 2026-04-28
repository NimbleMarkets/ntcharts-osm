// examples/mapview/main.go
//
// A two-pane mapview demo: a places list (top-left) plus details (bottom-left)
// alongside a live OpenStreetMap view (right). Tiles render via half-block
// glyphs (default) or the Kitty graphics protocol if your terminal supports it
// (Kitty, Ghostty, WezTerm).
//
// Keys:
//   arrows               pan the map
//   + / -                zoom in / out
//   shift-up/down, j/k   change list selection (map jumps to it automatically)
//   /                    filter the list
//   g                    toggle Glyph ↔ Kitty rendering
//   s                    toggle satellite ↔ graphics tiles
//   q / ctrl+c           quit
package main

import (
	"fmt"
	"os"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts-osm/mapview"
)

const (
	// Left column scales with terminal width up to leftColMaxInnerWidth, never
	// exceeding 70% of the total width (leftColMaxFractionPct).
	leftColMinInnerWidth = 30
	leftColMaxInnerWidth = 60
	leftColMaxFractionPct = 70
)

func styleName(s mapview.Style) string {
	switch s {
	case mapview.ArcgisWorldImagery:
		return "Satellite"
	default:
		return "Graphics"
	}
}

var (
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
)

// placeItem adapts mapview.Place to the bubbles/list DefaultItem interface.
type placeItem struct{ p mapview.Place }

func (i placeItem) Title() string {
	if i.p.Recommended {
		return "★ " + i.p.Name
	}
	return i.p.Name
}
func (i placeItem) Description() string { return i.p.Description }
func (i placeItem) FilterValue() string { return i.p.Name }

// mapKeyMap binds the map to arrow keys; zoom stays on +/-.
func mapKeyMap() mapview.KeyMap {
	return mapview.KeyMap{
		Up:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Right:   key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "right")),
		Down:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Left:    key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "left")),
		ZoomIn:  key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "zoom in")),
		ZoomOut: key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "zoom out")),
	}
}

// listKeyMap leaves arrow keys for the map; list cursor uses shift+up/down + j/k.
func listKeyMap() list.KeyMap {
	km := list.DefaultKeyMap()
	km.CursorUp = key.NewBinding(key.WithKeys("shift+up", "k"), key.WithHelp("⇧↑/k", "up"))
	km.CursorDown = key.NewBinding(key.WithKeys("shift+down", "j"), key.WithHelp("⇧↓/j", "down"))
	km.PrevPage = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "prev page"))
	km.NextPage = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "next page"))
	km.GoToStart = key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "first"))
	km.GoToEnd = key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "last"))
	km.Quit = key.NewBinding() // disabled — top level handles q / ctrl+c
	return km
}

// appKeys collects the bindings shown in the bottom help line. Most map and
// list keys are owned by the embedded models; this struct only exists so the
// help bubble has a single KeyMap to render from.
type appKeys struct {
	pan     key.Binding
	zoom    key.Binding
	listNav key.Binding
	filter  key.Binding
	mode    key.Binding
	style   key.Binding
	help    key.Binding
	quit    key.Binding
}

func newAppKeys() appKeys {
	return appKeys{
		pan:     key.NewBinding(key.WithKeys("up", "down", "left", "right"), key.WithHelp("←↑↓→", "pan")),
		zoom:    key.NewBinding(key.WithKeys("+", "-", "=", "_"), key.WithHelp("+/-", "zoom")),
		listNav: key.NewBinding(key.WithKeys("shift+up", "shift+down", "j", "k"), key.WithHelp("⇧↑↓/jk", "list")),
		filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		mode:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "mode")),
		style:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "style")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more")),
		quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k appKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.pan, k.zoom, k.listNav, k.filter, k.mode, k.style, k.help, k.quit}
}

func (k appKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.pan, k.zoom},
		{k.listNav, k.filter},
		{k.mode, k.style, k.help, k.quit},
	}
}

type model struct {
	mv            mapview.Model
	places        []mapview.Place
	list          list.Model
	help          help.Model
	keys          appKeys
	lastPlace     mapview.Place // most recently auto-applied selection
	width, height int
}

func initialModel() model {
	places, err := mapview.EmbeddedPlaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load places: %v\n", err)
		os.Exit(1)
	}
	if len(places) == 0 {
		fmt.Fprintln(os.Stderr, "no places to display")
		os.Exit(1)
	}

	items := make([]list.Item, len(places))
	for i, p := range places {
		items[i] = placeItem{p: p}
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Places"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.KeyMap = listKeyMap()

	first := places[0]
	// Oversample: 2 gives a 2× pixel-density tile-canvas at +1 OSM zoom,
	// which Kitty terminals downscale to a noticeably sharper image at no
	// extra geographic coverage. Glyph mode is unaffected visually.
	mv := mapview.NewWithConfig(mapview.Config{Oversample: 2})
	mv.KeyMap = mapKeyMap()
	mv.SetLatLng(first.Lat, first.Lon, first.Zoom)
	mv.SetMarkers([]mapview.Marker{{Lat: first.Lat, Lng: first.Lon}})

	return model{
		mv:        mv,
		places:    places,
		list:      l,
		help:      help.New(),
		keys:      newAppKeys(),
		lastPlace: first,
	}
}

// syncMapToSelection re-centers the map on the currently-highlighted place,
// returning a render Cmd if the selection actually changed.
func (m *model) syncMapToSelection() tea.Cmd {
	it, ok := m.list.SelectedItem().(placeItem)
	if !ok || it.p == m.lastPlace {
		return nil
	}
	m.lastPlace = it.p
	m.mv.SetLatLng(it.p.Lat, it.p.Lon, it.p.Zoom)
	m.mv.SetMarkers([]mapview.Marker{{Lat: it.p.Lat, Lng: it.p.Lon}})
	return m.mv.Init() // Init() returns the renderMapCmd
}

func (m model) Init() tea.Cmd { return m.mv.Init() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		L := m.layout()
		m.list.SetSize(L.leftInnerW, L.listInnerH)
		m.help.SetWidth(m.width)
		if c := m.mv.SetSize(L.mapInnerW, L.mapInnerH); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// While the list is in filter-input mode it owns the keyboard.
		if m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, tea.Batch(cmd, m.syncMapToSelection())
		}

		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "?":
			m.help.ShowAll = !m.help.ShowAll
			return m, nil
		case "g":
			mode := mapview.RenderKitty
			if m.mv.RenderMode() == mapview.RenderKitty {
				mode = mapview.RenderGlyph
			}
			return m, m.mv.SetRenderMode(mode)
		case "s":
			next := mapview.OpenStreetMaps
			if m.mv.TileStyle() != mapview.ArcgisWorldImagery {
				next = mapview.ArcgisWorldImagery
			}
			return m, m.mv.SetStyle(next)
		}

		// Forward to both — their KeyMaps are disjoint (arrows vs j/k/shift).
		var lcmd, mcmd tea.Cmd
		m.list, lcmd = m.list.Update(msg)
		m.mv, mcmd = m.mv.Update(msg)
		return m, tea.Batch(lcmd, mcmd, m.syncMapToSelection())
	}

	// Non-key messages (mapImageMsg, MapCoordinates, picture frames, etc.)
	// only need to reach mapview.
	var cmd tea.Cmd
	m.mv, cmd = m.mv.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// layout describes inner content dimensions (excluding borders) for each box.
type layoutDims struct {
	leftInnerW   int
	listInnerH   int
	detailInnerH int
	mapInnerW    int
	mapInnerH    int
}

// layout sizes the three boxes plus reserves rows for the status footer and
// the help line. Left column inner width grows with the terminal up to
// leftColMaxInnerWidth, capped at leftColMaxFractionPct of total width; the
// map then takes the remainder so its right border lands on the last column.
func (m model) layout() layoutDims {
	leftInnerW := m.width*leftColMaxFractionPct/100 - 2
	if leftInnerW > leftColMaxInnerWidth {
		leftInnerW = leftColMaxInnerWidth
	}
	if leftInnerW < leftColMinInnerWidth {
		leftInnerW = leftColMinInnerWidth
	}

	mapTotalH := m.height - 2 // 1 row footer + 1 row help
	if mapTotalH < 4 {
		mapTotalH = 4
	}
	detailOuterH := mapTotalH / 3
	if detailOuterH < 4 {
		detailOuterH = 4
	}
	listOuterH := mapTotalH - detailOuterH

	L := layoutDims{
		leftInnerW:   leftInnerW,
		listInnerH:   listOuterH - 2,
		detailInnerH: detailOuterH - 2,
		mapInnerW:    m.width - (leftInnerW + 2) - 2,
		mapInnerH:    mapTotalH - 2,
	}
	if L.listInnerH < 1 {
		L.listInnerH = 1
	}
	if L.detailInnerH < 1 {
		L.detailInnerH = 1
	}
	if L.mapInnerW < 1 {
		L.mapInnerW = 1
	}
	if L.mapInnerH < 1 {
		L.mapInnerH = 1
	}
	return L
}

func (m model) detailView(innerW, innerH int) string {
	it, ok := m.list.SelectedItem().(placeItem)
	if !ok {
		return ""
	}
	wrap := lipgloss.NewStyle().Width(innerW)
	parts := []string{
		titleStyle.Render(it.p.Name),
		wrap.Render(it.p.Description),
		dimStyle.Render(fmt.Sprintf("%.4f, %.4f  z%d", it.p.Lat, it.p.Lon, it.p.Zoom)),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Width(innerW).Height(innerH).Render(content)
}

func (m model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("loading…")
	}

	L := m.layout()

	listBox := boxStyle.Render(m.list.View())
	detailBox := boxStyle.Render(m.detailView(L.leftInnerW, L.detailInnerH))
	leftCol := lipgloss.JoinVertical(lipgloss.Left, listBox, detailBox)

	mapBox := boxStyle.Render(m.mv.View().Content)
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, mapBox)

	mode := "Glyph"
	if m.mv.RenderMode() == mapview.RenderKitty {
		mode = "Kitty"
	}
	lat, lng := m.mv.Center()
	status := fmt.Sprintf("%.4f,%.4f z%d  %s  %s",
		lat, lng, m.mv.Zoom(), mode, styleName(m.mv.TileStyle()))

	footer := footerStyle.Width(m.width).Render(status)
	helpView := m.help.View(m.keys)
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, footer, helpView))
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}
