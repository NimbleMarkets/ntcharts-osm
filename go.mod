module github.com/NimbleMarkets/ntcharts-osm

go 1.26.8

// Marker performance fixes; switch back when github.com/flopp/go-staticmaps includes them.
replace github.com/flopp/go-staticmaps => github.com/neomantra/go-staticmaps v0.0.0-20260929043700-f2bb572a3f0b

tool github.com/NimbleMarkets/go-booba/cmd/booba-assets

require (
	charm.land/bubbles/v2 v2.2.1
	charm.land/bubbletea/v2 v2.0.10
	charm.land/lipgloss/v2 v2.0.6
	github.com/NimbleMarkets/go-booba v0.7.0
	github.com/NimbleMarkets/ntcharts/v2 v2.4.0
	github.com/charmbracelet/harmonica v0.2.0
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/flopp/go-staticmaps v0.0.0-20260318105611-d3eb636a6468
	github.com/fogleman/gg v1.3.0
	github.com/golang/geo v0.0.0-20260928092222-7d12f68cfadb
)

require (
	github.com/NimbleMarkets/pixterm v0.0.0-20260501211346-dc18ac6c1a0f // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260928045949-bbf040aedf25 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/disintegration/imaging v1.6.2 // indirect
	github.com/flopp/go-coordsparser v0.0.0-20250311184423-61a7ff62d17c // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/mazznoer/csscolorparser v0.1.8 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sahilm/fuzzy v0.1.3 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tkrajina/gpxgo v1.5.1 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
