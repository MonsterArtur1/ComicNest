package panels

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"
	"time"
)

var (
	white = color.RGBA{255, 255, 255, 255}
	black = color.RGBA{0, 0, 0, 255}
	grey  = color.RGBA{128, 128, 128, 255}
)

// page is a synthetic comic page; panel rectangles are in pixels.
type page struct {
	img *image.RGBA
}

func newPage(w, h int, bg color.Color) *page {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	return &page{img: img}
}

func (p *page) fill(r image.Rectangle, c color.Color) {
	draw.Draw(p.img, r, &image.Uniform{c}, image.Point{}, draw.Src)
}

// panel draws a bordered panel with a white inside and some "art" in it.
func (p *page) panel(r image.Rectangle, border int) {
	p.fill(r, black)
	inner := r.Inset(border)
	p.fill(inner, white)
	// a figure and a ground line, not touching the border
	w, h := inner.Dx(), inner.Dy()
	p.fill(image.Rect(inner.Min.X+w/4, inner.Min.Y+h/4, inner.Min.X+w/2, inner.Min.Y+3*h/4), grey)
	p.fill(image.Rect(inner.Min.X+w/8, inner.Min.Y+4*h/5, inner.Max.X-w/8, inner.Min.Y+4*h/5+3), black)
}

// norm converts a pixel rectangle to normalized page coordinates.
func (p *page) norm(r image.Rectangle) Rect {
	b := p.img.Bounds()
	return Rect{
		X: float64(r.Min.X) / float64(b.Dx()), Y: float64(r.Min.Y) / float64(b.Dy()),
		W: float64(r.Dx()) / float64(b.Dx()), H: float64(r.Dy()) / float64(b.Dy()),
	}
}

func near(a, b Rect, tol float64) bool {
	return math.Abs(a.X-b.X) <= tol && math.Abs(a.Y-b.Y) <= tol &&
		math.Abs(a.W-b.W) <= tol && math.Abs(a.H-b.H) <= tol
}

// assertPanels checks got matches want, in order, within 1.5% of the page.
func assertPanels(t *testing.T, got, want []Rect) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d panels, want %d:\n got  %v\n want %v", len(got), len(want), got, want)
	}
	for i := range want {
		if !near(got[i], want[i], 0.015) {
			t.Errorf("panel %d: got %+v, want about %+v", i, got[i], want[i])
		}
	}
}

func assertFullPage(t *testing.T, got []Rect) {
	t.Helper()
	if len(got) != 1 || got[0] != FullPage {
		t.Fatalf("got %v, want the single full-page panel", got)
	}
}

// classicGrid is a 2 / 1 / 3 panel layout on an 800x1200 page.
func classicGrid() []image.Rectangle {
	return []image.Rectangle{
		image.Rect(40, 40, 380, 400), image.Rect(410, 40, 760, 400),
		image.Rect(40, 430, 760, 780),
		image.Rect(40, 810, 253, 1160), image.Rect(283, 810, 496, 1160), image.Rect(526, 810, 760, 1160),
	}
}

func drawGrid(rects []image.Rectangle, border int) *page {
	p := newPage(800, 1200, white)
	for _, r := range rects {
		p.panel(r, border)
	}
	return p
}

func expected(p *page, rects []image.Rectangle, order ...int) []Rect {
	if len(order) == 0 {
		for i := range rects {
			order = append(order, i)
		}
	}
	var out []Rect
	for _, i := range order {
		out = append(out, p.norm(rects[i]))
	}
	return out
}

func TestClassicGridLTR(t *testing.T) {
	rects := classicGrid()
	p := drawGrid(rects, 4)
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestThinBordersWhiteInside(t *testing.T) {
	// 2px borders at 1200px become ~1px at the analysis size; the white
	// insides must not be mistaken for gutter.
	rects := classicGrid()
	p := drawGrid(rects, 2)
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestRightToLeftOrder(t *testing.T) {
	rects := classicGrid()
	p := drawGrid(rects, 4)
	assertPanels(t, Detect(p.img, true, Options{}), expected(p, rects, 1, 0, 2, 5, 4, 3))
}

func TestBlackGutters(t *testing.T) {
	p := newPage(900, 1300, black)
	rects := []image.Rectangle{
		image.Rect(30, 30, 440, 640), image.Rect(470, 30, 870, 640),
		image.Rect(30, 670, 440, 1270), image.Rect(470, 670, 870, 1270),
	}
	for _, r := range rects {
		p.fill(r, grey)
		in := r.Inset(60)
		p.fill(image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+in.Dy()/3), white)
	}
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestSplashPage(t *testing.T) {
	p := newPage(800, 1200, white)
	p.panel(image.Rect(30, 30, 770, 1170), 5)
	assertFullPage(t, Detect(p.img, false, Options{}))
}

func TestFullBleedArt(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	p := newPage(800, 1200, white)
	for y := 0; y < 1200; y += 8 {
		for x := 0; x < 800; x += 8 {
			v := uint8(rng.Intn(256))
			p.fill(image.Rect(x, y, x+8, y+8), color.RGBA{v, v / 2, 255 - v, 255})
		}
	}
	assertFullPage(t, Detect(p.img, false, Options{}))
}

func TestNoiseAndPageNumber(t *testing.T) {
	rects := []image.Rectangle{
		image.Rect(40, 40, 760, 560),
		image.Rect(40, 600, 390, 1120), image.Rect(420, 600, 760, 1120),
	}
	p := drawGrid(rects, 4)
	// speckles all over the gutters
	rng := rand.New(rand.NewSource(2))
	for range 600 {
		x, y := rng.Intn(800), rng.Intn(1200)
		p.fill(image.Rect(x, y, x+1, y+1), black)
	}
	// a page number centred under the panels
	p.fill(image.Rect(390, 1150, 410, 1170), black)
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestCaptionSliverMerged(t *testing.T) {
	// A narrow full-width caption box between two rows belongs to a panel,
	// it is not a panel of its own.
	rects := []image.Rectangle{
		image.Rect(40, 40, 760, 560),
		image.Rect(40, 640, 760, 1160),
	}
	p := drawGrid(rects, 4)
	caption := image.Rect(40, 580, 760, 615)
	p.fill(caption, black)
	p.fill(caption.Inset(2), color.RGBA{250, 240, 180, 255})
	got := Detect(p.img, false, Options{})
	if len(got) != 2 {
		t.Fatalf("got %d panels, want 2: %v", len(got), got)
	}
	if want := p.norm(rects[0]); !near(got[0], want, 0.015) {
		t.Errorf("first panel: got %+v, want %+v", got[0], want)
	}
	// the caption merged into the second panel, which now starts at it
	if want := p.norm(caption.Union(rects[1])); !near(got[1], want, 0.015) {
		t.Errorf("second panel: got %+v, want %+v", got[1], want)
	}
}

func TestTooManyPanels(t *testing.T) {
	p := newPage(800, 1200, white)
	for row := range 6 {
		for col := range 5 {
			x, y := 20+col*155, 20+row*195
			p.panel(image.Rect(x, y, x+140, y+180), 3)
		}
	}
	assertFullPage(t, Detect(p.img, false, Options{}))
}

func TestDoublePageSpread(t *testing.T) {
	p := newPage(1600, 1200, white)
	var rects []image.Rectangle
	for row := range 2 {
		for col := range 4 {
			x, y := 40+col*390, 40+row*570
			rects = append(rects, image.Rect(x, y, x+360, y+540))
		}
	}
	for _, r := range rects {
		p.panel(r, 4)
	}
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestDetectBytesJPEG(t *testing.T) {
	// Full-size, JPEG-compressed (YCbCr fast path, compression noise).
	scaled := make([]image.Rectangle, 0, 6)
	for _, r := range classicGrid() {
		scaled = append(scaled, image.Rect(r.Min.X*2, r.Min.Y*2, r.Max.X*2, r.Max.Y*2))
	}
	p := newPage(1600, 2400, white)
	for _, r := range scaled {
		p.panel(r, 8)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, p.img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := DetectBytes(buf.Bytes(), false, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("decode + detect of a 1600x2400 JPEG: %v", time.Since(start))
	assertPanels(t, got, expected(p, scaled))

	if _, err := DetectBytes([]byte("not an image"), false, Options{}); err == nil {
		t.Error("garbage input: want an error")
	}
}

func TestTinyImage(t *testing.T) {
	assertFullPage(t, Detect(image.NewRGBA(image.Rect(0, 0, 4, 4)), false, Options{}))
}

func TestBalloonCrossingGutter(t *testing.T) {
	// A speech balloon straddling the vertical gutter used to block the cut
	// (the band had to be 98% gutter).
	rects := []image.Rectangle{
		image.Rect(40, 40, 380, 560), image.Rect(420, 40, 760, 560),
		image.Rect(40, 600, 760, 1160),
	}
	p := drawGrid(rects, 4)
	balloon := image.Rect(300, 150, 500, 230)
	p.fill(balloon, black)
	p.fill(balloon.Inset(2), white)
	// The balloon is part of the first panel's art; the second panel's box
	// is unaffected.
	got := Detect(p.img, false, Options{})
	if len(got) != 3 {
		t.Fatalf("got %d panels, want 3: %v", len(got), got)
	}
	for i, want := range []Rect{p.norm(rects[0]), p.norm(rects[1]), p.norm(rects[2])} {
		if !near(got[i], want, 0.03) {
			t.Errorf("panel %d: got %+v, want about %+v", i, got[i], want)
		}
	}
}

// artPanel fills r with blocky "art" in mid tones (no border).
func (p *page) artPanel(r image.Rectangle, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	for y := r.Min.Y; y < r.Max.Y; y += 20 {
		for x := r.Min.X; x < r.Max.X; x += 20 {
			v := uint8(60 + rng.Intn(120))
			p.fill(image.Rect(x, y, min(x+20, r.Max.X), min(y+20, r.Max.Y)), color.RGBA{v, v / 2, 200 - v/2, 255})
		}
	}
}

func TestBleedPanel(t *testing.T) {
	// The top panel bleeds off three page edges; the frame is still mostly
	// white, the remaining panels are found below it.
	p := newPage(800, 1200, white)
	top := image.Rect(0, 0, 800, 520)
	p.artPanel(top, 3)
	rects := []image.Rectangle{top, image.Rect(40, 560, 390, 1160), image.Rect(420, 560, 760, 1160)}
	p.panel(rects[1], 4)
	p.panel(rects[2], 4)
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestBorderlessPanelsOnColour(t *testing.T) {
	// Flat, borderless panels on a beige page.
	p := newPage(900, 1300, color.RGBA{232, 216, 172, 255})
	rects := []image.Rectangle{
		image.Rect(50, 50, 430, 620), image.Rect(470, 50, 850, 620),
		image.Rect(50, 660, 850, 1250),
	}
	for i, r := range rects {
		p.artPanel(r, int64(10+i))
	}
	assertPanels(t, Detect(p.img, false, Options{}), expected(p, rects))
}

func TestTouchingPanelsSplitAtBorderLine(t *testing.T) {
	// Two panels drawn touching, sharing one border line, no gutter between.
	p := newPage(800, 1200, white)
	p.fill(image.Rect(40, 40, 760, 1160), black)
	upper, lower := image.Rect(44, 44, 756, 596), image.Rect(44, 600, 756, 1156)
	for _, r := range []image.Rectangle{upper, lower} {
		p.fill(r, white)
		w, h := r.Dx(), r.Dy()
		p.fill(image.Rect(r.Min.X+w/4, r.Min.Y+h/4, r.Min.X+w/2, r.Min.Y+3*h/4), grey)
	}
	want := []Rect{p.norm(image.Rect(40, 40, 760, 598)), p.norm(image.Rect(40, 598, 760, 1160))}
	assertPanels(t, Detect(p.img, false, Options{}), want)
}

// obstructedRow draws four borderless panels in a row whose three gutters
// are bridged by art over 4%, 12% and 22% of their height.
func obstructedRow() *page {
	p := newPage(1000, 1200, white)
	xs := []int{40, 270, 500, 730, 960}
	for i := range 4 {
		p.fill(image.Rect(xs[i], 50, xs[i+1]-30, 1150), grey)
	}
	for i, share := range []float64{0.04, 0.12, 0.22} {
		h := int(share * 1100)
		x := xs[i+1] - 40
		p.fill(image.Rect(x, 400, x+50, 400+h), color.RGBA{150, 150, 150, 255})
	}
	return p
}

func TestSensitivityMonotonic(t *testing.T) {
	p := obstructedRow()
	counts := map[string]int{}
	for _, s := range []string{SensitivityLow, SensitivityNormal, SensitivityHigh} {
		counts[s] = len(Detect(p.img, false, Options{Sensitivity: s}))
	}
	if counts["low"] != 2 || counts["normal"] != 3 || counts["high"] != 4 {
		t.Errorf("panels per sensitivity = %v, want low 2, normal 3, high 4", counts)
	}
	if def := len(Detect(p.img, false, Options{})); def != counts["normal"] {
		t.Errorf("default options found %d panels, normal %d", def, counts["normal"])
	}
}

func TestOptionsCacheVersion(t *testing.T) {
	base := Options{}.CacheVersion()
	if base/1000 != Version {
		t.Errorf("CacheVersion %d does not encode Version %d", base, Version)
	}
	if v := (Options{Sensitivity: "Normal"}).CacheVersion(); v != base {
		t.Errorf(`"Normal" = %d, default = %d: same parameters, want the same version`, v, base)
	}
	seen := map[int]Options{base: {}}
	for _, o := range []Options{
		{Sensitivity: SensitivityLow}, {Sensitivity: SensitivityHigh},
		{GutterTolerance: 25}, {MinPanelArea: 0.05},
	} {
		v := o.CacheVersion()
		if prev, dup := seen[v]; dup {
			t.Errorf("%+v and %+v share cache version %d", o, prev, v)
		}
		seen[v] = o
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, o := range []Options{{}, {Sensitivity: "HIGH"}, {GutterTolerance: 60, MinPanelArea: 0.01}} {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	for _, o := range []Options{{Sensitivity: "max"}, {GutterTolerance: -2}, {MinPanelArea: 0.7}} {
		if err := o.Validate(); err == nil {
			t.Errorf("%+v: want an error", o)
		}
	}
}
