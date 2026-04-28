# ntcharts-osm — Terminal OpenStreetMap widget for Bubble Tea

<p>
    <a href="https://github.com/NimbleMarkets/ntcharts-osm/tags"><img src="https://img.shields.io/github/tag/NimbleMarkets/ntcharts-osm.svg" alt="Latest Release"></a>
    <a href="https://pkg.go.dev/github.com/NimbleMarkets/ntcharts-osm?tab=doc"><img src="https://godoc.org/github.com/golang/gddo?status.svg" alt="GoDoc"></a>
    <a href="https://github.com/NimbleMarkets/ntcharts-osm/blob/main/CODE_OF_CONDUCT.md"><img src="https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa.svg"  alt="Code Of Conduct"></a>
</p>

`ntcharts-osm` is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) widget that renders OpenStreetMap tiles in the terminal. It pairs [`flopp/go-staticmaps`](https://github.com/flopp/go-staticmaps) for tile fetching with [`ntcharts/v2/picture`](https://github.com/NimbleMarkets/ntcharts) for image rendering — half-block glyphs anywhere, full-resolution Kitty graphics on terminals that support them (Kitty, Ghostty, WezTerm).

## Quickstart

```go
package main

import (
    "fmt"
    "os"

    tea "charm.land/bubbletea/v2"
    "github.com/NimbleMarkets/ntcharts-osm/mapview"
)

type model struct{ mv mapview.Model }

func (m model) Init() tea.Cmd { return m.mv.Init() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if k, ok := msg.(tea.KeyMsg); ok && (k.String() == "q" || k.String() == "ctrl+c") {
        return m, tea.Quit
    }
    if sz, ok := msg.(tea.WindowSizeMsg); ok {
        m.mv.SetSize(sz.Width, sz.Height)
    }
    var cmd tea.Cmd
    m.mv, cmd = m.mv.Update(msg)
    return m, cmd
}

func (m model) View() tea.View { return m.mv.View() }

func main() {
    mv := mapview.New(0, 0)
    mv.SetLatLng(40.6892, -74.0445, 13) // Statue of Liberty
    if _, err := tea.NewProgram(model{mv: mv}).Run(); err != nil {
        fmt.Println(err); os.Exit(1)
    }
}
```

Pan with arrows or `hjkl`, zoom with `+`/`-`. The widget owns those bindings — no parent wiring needed.

## Demo

A fuller single-pane demo lives at [`examples/mapview`](./examples/mapview/main.go) — adds tile-style cycling and Glyph/Kitty mode toggling.

```sh
task build-ex-mapview
./bin/ntcharts-osm-mapview
```

## Tile styles

`mapview.SetStyle(...)` switches between nine tile providers from [`flopp/go-staticmaps`](https://github.com/flopp/go-staticmaps): `Wikimedia`, `OpenStreetMaps`, `OpenTopoMap`, `OpenCycleMap`, `CartoLight`, `CartoDark`, `StamenToner`, `StamenTerrain`, `ArcgisWorldImagery`. Each comes with the upstream provider's terms-of-use and attribution requirements — read them before shipping a public app.

## Render modes

| Mode | What it does | Where it works |
|---|---|---|
| `mapview.RenderGlyph` (default) | Half-block ANSI from [`pixterm/ansimage`](https://github.com/eliukblau/pixterm), via `ntcharts/v2/picture` | Any modern terminal |
| `mapview.RenderKitty` | Full-resolution image via Kitty graphics protocol | Kitty, Ghostty, WezTerm |

`mv.SetRenderMode(mode)` returns a `tea.Cmd` that re-renders at the new mode. Toggling away from Kitty automatically deletes the uploaded image so no ghost stays in the terminal.

## Bubble Tea version

Targets Bubble Tea **v2** (`charm.land/bubbletea/v2`). No v1 backport.

## Known caveats

- **Tile fetching is synchronous.** `osm.Render()` runs in a goroutine that the widget dispatches via `tea.Cmd`, but `sm.Context` itself is not goroutine-safe — concurrent renders (e.g. a resize storm) race on the same context. For most TUIs this is fine; if you're driving thousands of renders/sec, snapshot the state into closures before dispatching.
- **No built-in API key handling.** Tile providers that require a key (e.g. Thunderforest) are not bundled — add a `replace` and a custom `tileProvider` if you need one.
- **Geocoding uses Nominatim** with no caching. Respect their [usage policy](https://operations.osmfoundation.org/policies/nominatim/) for production traffic.

## License

[MIT License](./LICENSE.txt) — Copyright (c) 2024-2026 [Neomantra Corp](https://www.neomantra.com).

----
Made with :heart: and :fire: by the team behind [Nimble.Markets](https://nimble.markets).
