package mapview

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	sm "github.com/flopp/go-staticmaps"
	"github.com/fogleman/gg"
	"github.com/golang/geo/s2"
)

const maxMercatorLatitude = 85.0511287798066
const panRenderInterval = time.Second / 30

// worldPixels converts latitude and longitude to Web Mercator pixels.
func worldPixels(lat, lng float64, zoom, tileSize int) (x, y, world float64) {
	world = math.Exp2(float64(zoom)) * float64(tileSize)
	lat = max(-maxMercatorLatitude, min(maxMercatorLatitude, lat))
	x = (lng + 180) / 360 * world
	y = (1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2 * world
	return
}

func (m *Model) panPixels(dx, dy float64) {
	tileSize := m.tileProvider.TileSize
	x, y, world := worldPixels(m.lat, m.lng, m.zoom, tileSize)
	// Compensate for optical zoom to keep the on-screen pan distance constant.
	factor := float64(opticalCropFactor(m.opticalZoom))
	x = math.Mod(math.Mod(x+dx/factor, world)+world, world)
	y = max(0, min(world, y+dy/factor))
	m.lng = x/world*360 - 180
	m.lat = math.Atan(math.Sinh(math.Pi*(1-2*y/world))) * 180 / math.Pi
}

// Only the Bubble Tea event loop changes panState. Commands use its pointer
// to identify the model and return buffers without modifying this state.
type panState struct {
	motion                      panMotion
	buffer                      *panBuffer
	busy, waiting               bool
	lastStart                   time.Time
	target, displayed           renderKey
	targetSet, displayedSet     bool
	visible                     image.Image
	encoding, presentationDirty bool
}

type panRenderTickMsg struct{ owner *panState }
type panPictureMsg struct {
	owner *panState
	msg   tea.Msg
}
type panPresentedMsg struct{ owner *panState }
type bufferImageMsg struct {
	owner  *panState
	buffer *panBuffer
	err    error
}

type panBuffer struct {
	key                 renderKey
	img                 image.Image
	centerX, centerY    float64
	world               float64
	mapW, mapH, padding int
	tileSize            int
	attribution         string
}

func (b *panBuffer) matches(key renderKey) bool {
	if b == nil {
		return false
	}
	other := b.key
	other.lat, other.lng = key.lat, key.lng
	return other == key
}

func (b *panBuffer) viewport(key renderKey) (image.Rectangle, bool) {
	if !b.matches(key) || b.img == nil {
		return image.Rectangle{}, false
	}
	x, y, _ := worldPixels(key.lat, key.lng, key.zoom+log2(key.oversample), b.tileSize)
	dx := x - b.centerX
	dx -= math.Floor(dx/b.world+0.5) * b.world
	// Match go-staticmaps by rounding the center down to whole pixels.
	ox := b.padding + int(math.Floor(b.centerX+dx)-math.Floor(b.centerX))
	oy := b.padding + int(math.Floor(y)-math.Floor(b.centerY))
	r := image.Rect(ox, oy, ox+b.mapW, oy+b.mapH)
	return r, r.In(b.img.Bounds())
}

func (b *panBuffer) nearEdge(r image.Rectangle) bool {
	margin := b.padding / 2
	bounds := b.img.Bounds()
	return r.Min.X < bounds.Min.X+margin || r.Min.Y < bounds.Min.Y+margin ||
		r.Max.X > bounds.Max.X-margin || r.Max.Y > bounds.Max.Y-margin
}

func (b *panBuffer) crop(r image.Rectangle, key renderKey, bg color.Color) image.Image {
	// Copy the viewport so cached views can release the full buffer
	// and attribution can be drawn without changing it.
	img := image.NewRGBA(image.Rect(0, 0, b.mapW, b.mapH))
	draw.Draw(img, img.Bounds(), b.img, r.Min, draw.Src)
	drawMapAttribution(img, b.attribution)
	return composeLetterbox(img, key.cols*osmPxPerCellW*key.oversample,
		key.rows*osmPxPerCellH*key.oversample, bg)
}

func (m *Model) currentRenderKey() renderKey {
	os, _ := m.effectiveOversample()
	return makeRenderKey(m.lat, m.lng, m.zoom, m.cols, m.picRows(), m.tileStyle,
		os, m.maxAspectRatio, m.letterboxColor, m.markers)
}

func (m *Model) renderBufferedMapCmd() tea.Cmd {
	state := m.panState
	key := m.currentRenderKey()
	if !state.targetSet || state.target != key {
		state.target, state.targetSet = key, true
		*m.renderGen++
	}

	var present tea.Cmd
	r, covered := state.buffer.viewport(key)
	if !state.displayedSet || state.displayed != key {
		var img image.Image
		if covered {
			img = state.buffer.crop(r, key, m.letterboxColor)
		} else if m.cache != nil {
			img, _ = m.cache.get(key)
		}
		if img != nil {
			state.visible = img
			state.displayed, state.displayedSet = key, true
			if m.cache != nil && !state.motion.active {
				m.cache.put(key, img)
			}
			present = m.presentPanImage(opticalCrop(img, opticalCropFactor(m.opticalZoom)))
		}
	}
	if state.displayedSet && state.displayed == key {
		m.sourceImage = state.visible
		m.errMsg = ""
		*m.lastAccepted = *m.renderGen
	}
	if covered && !state.buffer.nearEdge(r) {
		return present
	}
	if state.busy || state.waiting {
		return present
	}

	// Limit fetch frequency. New input does not delay a scheduled fetch.
	if delay := time.Until(state.lastStart.Add(panRenderInterval)); delay > 0 {
		state.waiting = true
		return tea.Batch(present, tea.Tick(delay, func(time.Time) tea.Msg {
			return panRenderTickMsg{owner: state}
		}))
	}
	state.busy = true
	state.lastStart = time.Now()
	provider := *m.tileProvider
	markers := append([]Marker(nil), m.markers...)
	w, h := targetMapDims(key.cols*osmPxPerCellW*key.oversample,
		key.rows*osmPxPerCellH*key.oversample, key.maxAspectRatio)
	padding := m.panBufferTiles * provider.TileSize
	x, y, world := worldPixels(key.lat, key.lng, key.zoom+log2(key.oversample), provider.TileSize)
	return tea.Batch(present, func() tea.Msg {
		ctx := sm.NewContext()
		configureTileCache(ctx)
		ctx.SetTileProvider(&provider)
		ctx.SetCenter(s2.LatLngFromDegrees(key.lat, key.lng))
		ctx.SetZoom(key.zoom + log2(key.oversample))
		ctx.SetSize(w+2*padding, h+2*padding)
		// Draw attribution after cropping to the visible viewport.
		ctx.OverrideAttribution("")
		addMapMarkers(ctx, markers, key.lat, key.lng, key.zoom+log2(key.oversample), provider.TileSize)
		img, err := ctx.Render()
		return bufferImageMsg{owner: state, err: err, buffer: &panBuffer{
			key: key, img: img, centerX: x, centerY: y, world: world,
			mapW: w, mapH: h, padding: padding, tileSize: provider.TileSize,
			attribution: provider.Attribution,
		}}
	})
}

// Finish sending each Kitty frame before encoding the latest view.
// Otherwise, rapid input can invalidate every frame before it is sent.
func (m *Model) presentPanImage(img image.Image) tea.Cmd {
	state := m.panState
	if state.encoding {
		state.presentationDirty = true
		return nil
	}
	cmd := m.pic.SetImage(img)
	if cmd == nil {
		return nil
	}
	state.encoding = true
	return func() tea.Msg { return panPictureMsg{owner: state, msg: cmd()} }
}

// safeMarker corrects offscreen marker wrapping in older go-staticmaps versions.
type safeMarker struct {
	*sm.Marker
	center s2.LatLng
	world  float64
}

func (m *safeMarker) Draw(gc *gg.Context, trans *sm.Transformer) {
	if !sm.CanDisplay(m.Position) {
		return
	}
	x, y := trans.LatLngToXY(m.Position)
	cx, _ := trans.LatLngToXY(m.center)
	shift := -math.Floor((x-cx)/m.world+0.5) * m.world
	left, top, right, bottom := m.ExtraMarginPixels()
	if x+shift+right < 0 || x+shift-left > float64(gc.Width()) ||
		y+bottom < 0 || y-top > float64(gc.Height()) {
		return
	}
	gc.Push()
	gc.Translate(shift, 0)
	m.Marker.Draw(gc, trans)
	gc.Pop()
}

func addMapMarkers(ctx *sm.Context, markers []Marker, lat, lng float64, zoom, tileSize int) {
	for _, mk := range markers {
		col := mk.Color
		if col == nil {
			col = color.RGBA{255, 0, 0, 255}
		}
		size := mk.Size
		if size == 0 {
			size = 16
		}
		ctx.AddObject(&safeMarker{
			Marker: sm.NewMarker(s2.LatLngFromDegrees(mk.Lat, mk.Lng), col, size),
			center: s2.LatLngFromDegrees(lat, lng),
			world:  math.Exp2(float64(zoom)) * float64(tileSize),
		})
	}
}

// Draw attribution at the bottom of the cropped map using go-staticmaps styling.
func drawMapAttribution(img *image.RGBA, attribution string) {
	if attribution == "" {
		return
	}
	gc := gg.NewContextForRGBA(img)
	lines := strings.Split(attribution, "\n")
	var lineHeight float64
	for _, line := range lines {
		_, h := gc.MeasureString(line)
		lineHeight = max(lineHeight, h)
	}
	height := (lineHeight+2)*float64(len(lines)) + 2
	y := float64(img.Bounds().Dy()) - height
	gc.SetRGBA(0, 0, 0, .5)
	gc.DrawRectangle(0, y, float64(img.Bounds().Dx()), height)
	gc.Fill()
	gc.SetRGBA(1, 1, 1, .75)
	for _, line := range lines {
		gc.DrawStringAnchored(line, 2, y, 0, 1)
		y += lineHeight + 2
	}
}
