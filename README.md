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

## Heads-up: pixterm replace

Glyph rendering goes through `ntcharts/v2/picture` → `eliukblau/pixterm/pkg/ansimage`. The upstream `RenderExt` has an off-by-one that drops the first cell row, so the rendered map ends up one row shorter than the surrounding box. Until the fix is merged upstream, this module's `go.mod` carries the same replace ntcharts uses — pointing at the NimbleMarkets fork:

```go.mod
replace github.com/eliukblau/pixterm => github.com/NimbleMarkets/pixterm v0.0.0-20260428212147-d576e057b538
```

Go's module system **does not propagate replace directives transitively**, so any consumer of this module needs to add the same replace in its own `go.mod`. Once the upstream pixterm release includes the fix, both replaces can come out.

## Known caveats

- **Tile fetching is synchronous per render.** Each render builds its own `*sm.Context` inside the dispatched goroutine and tags the result with a generation counter, so rapid pan / zoom / resize fires safely-parallel renders and stale results are dropped. Composited images are kept in a small per-Model LRU keyed on `(lat, lng, zoom, cols, rows, style, oversample, markers)` — revisiting a state hits the cache synchronously (no goroutine, no Loading overlay). Default cap is 16 entries; tune via `mapview.NewWithConfig(Config{CacheCap: N})` (`-1` disables caching).
- **Optical zoom for Kitty graphics.** `Config.Oversample` raises the source-image pixel density without changing visible geographic coverage — `Oversample: N` (powers of 2) renders the same area at `N×` per-cell resolution and `+log2(N)` OSM tile zoom, so Kitty terminals can downscale a sharper source. `1` (default) keeps current behavior; `2` is a noticeable boost; `4` is hi-DPI quality at ~16× the tile fetches. Capped so the effective tile zoom never exceeds 19. Glyph mode pays the cost without visible benefit.
- **No built-in API key handling.** Tile providers that require a key (e.g. Thunderforest) are not bundled — add a custom `tileProvider` if you need one.
- **Geocoding uses Nominatim** with no caching. Respect their [usage policy](https://operations.osmfoundation.org/policies/nominatim/) for production traffic.

## License

[MIT License](./LICENSE.txt) — Copyright (c) 2024-2026 [Neomantra Corp](https://www.neomantra.com).

----
Made with :heart: and :fire: by the team behind [Nimble.Markets](https://nimble.markets).
