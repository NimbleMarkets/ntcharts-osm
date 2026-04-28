// examples/mapview/main.go
//
// A simple single-pane mapview demo, centered on the Statue of Liberty.
// Renders OpenStreetMap tiles via half-block glyphs (default) or the Kitty
// graphics protocol if your terminal supports it (Kitty, Ghostty, WezTerm).
//
// Keys:
//   arrows / hjkl   pan
//   + / -           zoom in / out
//   g               toggle Glyph ↔ Kitty rendering
//   s               cycle tile style
//   q / ctrl+c      quit
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts-osm/mapview"
)

const (
	initialLat  = 40.6892
	initialLng  = -74.0445
	initialZoom = 13

	// 9 tile styles: Wikimedia, OpenStreetMaps, OpenTopoMap, OpenCycleMap,
	// CartoLight, CartoDark, StamenToner, StamenTerrain, ArcgisWorldImagery.
	numStyles = 9
)

var styleNames = [numStyles]string{
	"Wikimedia",
	"OpenStreetMaps",
	"OpenTopoMap",
	"OpenCycleMap",
	"CartoLight",
	"CartoDark",
	"StamenToner",
	"StamenTerrain",
	"ArcgisWorldImagery",
}

type model struct {
	mv            mapview.Model
	width, height int
}

func initialModel() model {
	mv := mapview.New(0, 0)
	mv.SetLatLng(initialLat, initialLng, initialZoom)
	mv.SetMarkers([]mapview.Marker{{Lat: initialLat, Lng: initialLng}})
	return model{mv: mv}
}

func (m model) Init() tea.Cmd { return m.mv.Init() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "g":
			mode := mapview.RenderKitty
			if m.mv.RenderMode() == mapview.RenderKitty {
				mode = mapview.RenderGlyph
			}
			return m, m.mv.SetRenderMode(mode)
		case "s":
			next := mapview.Style((int(m.mv.TileStyle()) + 1) % numStyles)
			return m, m.mv.SetStyle(next)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Reserve 1 row for the footer.
		if c := m.mv.SetSize(m.width, m.height-1); c != nil {
			cmds = append(cmds, c)
		}
	}

	var cmd tea.Cmd
	m.mv, cmd = m.mv.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
	body := m.mv.View().Content

	mode := "Glyph"
	if m.mv.RenderMode() == mapview.RenderKitty {
		mode = "Kitty"
	}
	lat, lng := m.mv.Center()
	style := styleNames[int(m.mv.TileStyle())%numStyles]

	footer := lipgloss.NewStyle().
		Width(m.width).
		Foreground(lipgloss.Color("242")).
		Render(fmt.Sprintf(
			"%.4f,%.4f  z%d  %s  %s   arrows/hjkl pan, +/- zoom, g mode, s style, q quit",
			lat, lng, m.mv.Zoom(), mode, style,
		))

	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, footer))
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}
