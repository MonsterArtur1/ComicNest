// Package panels finds the panels of a comic page for the reader's guided
// view (panel-by-panel reading).
//
// The detection is classic gutter-based segmentation, no machine learning:
//
//  1. the page is converted to grayscale and box-averaged down to at most
//     analysisSize pixels on its long side (enough to keep the thin gutters
//     of European albums, ~0.5% of the page, a few pixels wide);
//  2. the gutter colour is the dominant shade of the page's outer frame —
//     white, black and coloured gutters all work, and bleed panels touching
//     some page edges don't matter as long as enough of the frame agrees; when
//     no shade dominates (full-bleed art) the page is a single panel;
//  3. gutter pixels are the background-coloured pixels flood-filled from the
//     page's outer frame, so the (often white) inside of a bordered panel or
//     a speech balloon counts as content rather than gutter;
//  4. the content mask is eroded to cut the thin bridges (balloon outlines,
//     art lines) that cross gutters;
//  5. recursive XY-cut of the eroded content along gutter bands that are
//     mostly — not necessarily entirely — gutter, so a balloon or a bit of
//     art overlapping a gutter no longer hides it (inside pieces whose art
//     is itself largely gutter-coloured, e.g. black gutters next to black
//     ink, only clean bands bounded by straight panel edges count);
//  6. a piece no straight band splits is split into its connected
//     components when they are solid boxes (overlapping or L-shaped
//     layouts), else along the dark border lines of panels drawn touching
//     each other (light pages), else along inner gutters of another colour
//     (white gutters inside a frame on a dark page);
//  7. specks are dropped, contained or heavily overlapping boxes merged,
//     thin slivers (caption strips) merged into a neighbour, and the panels
//     ordered by an XY-cut over the boxes: rows top to bottom, panels left to
//     right within a row (right to left for manga). Zero, one or implausibly
//     many panels mean "single full-page panel" — the reader then pages
//     normally.
package panels

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"math"
	"sort"
	"strings"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/webp" // register WebP decoder
)

// Version identifies the detection algorithm. Bump it whenever the output
// changes so cached results computed by an older version get recomputed (see
// Options.CacheVersion, which also folds in the options).
const Version = 2

const (
	// analysisSize is the long side pages are scaled down to for analysis.
	analysisSize = 1200
	// frameAgreeTolerance is how close (in gray levels) a frame pixel must be
	// to the dominant frame shade to count as agreeing with it.
	frameAgreeTolerance = 14
	// minTolerance is the tightest gutter colour tolerance used for flat
	// (digital) gutters.
	minTolerance = 14
	// minFrameAgreement is the share of the page's outer frame that must
	// match the gutter colour; below it the page is full-bleed art.
	minFrameAgreement = 0.3
	// trimFraction is the share of a row (column) that must be gutter for it
	// to be trimmed off the edge of a panel.
	trimFraction = 0.98
	// edgeSearch and edgeContent: in a leaky region a gutter band needs, on
	// both sides and along at least edgeContent of its length, content within
	// edgeSearch lines of it (a straight panel edge).
	edgeSearch  = 4
	edgeContent = 0.75
	// darkLevel: pixels darker than this are border-ish ink (light pages).
	darkLevel = 100
	// lineDark is the share of a border line that must be dark; lineSide the
	// share a line a few pixels beside it may be dark at most.
	lineDark = 0.92
	lineSide = 0.6
	// maxLineDepth bounds the border-line splitting recursion.
	maxLineDepth = 3
	// minSolidity is the share of its bounding box a connected component
	// must fill to count as a panel of its own (see componentSplit).
	minSolidity = 0.8
	// sliverFraction marks regions thinner than this share of the page's
	// width or height as slivers to merge into a neighbour.
	sliverFraction = 0.045
	// minCoverage: panels covering less than this share of the page are
	// implausible (mostly empty page) — fall back to the full page.
	minCoverage = 0.2
	// maxPanels: more panels than this are implausible.
	maxPanels = 20
	// maxDepth bounds the XY-cut recursion.
	maxDepth = 8
	// maxDimension is the largest source width or height accepted.
	maxDimension = 12000
)

// Sensitivity presets for Options.Sensitivity.
const (
	SensitivityLow    = "low"
	SensitivityNormal = "normal"
	SensitivityHigh   = "high"
)

// Options tune the detection. The zero value is the default ("normal").
type Options struct {
	// Sensitivity is low, normal (default, also "") or high. Higher splits
	// more aggressively: it finds more panels, at the price of more false
	// splits on busy art.
	Sensitivity string
	// GutterTolerance overrides how far (0..255 gray levels) a pixel may be
	// from the gutter colour and still count as gutter (0 = preset default).
	GutterTolerance int
	// MinPanelArea overrides the smallest panel, as a fraction of the page
	// area (0 = preset default).
	MinPanelArea float64
}

// params are the effective detection parameters derived from Options.
type params struct {
	tol     int     // gutter colour tolerance
	band    float64 // share of a line that must be gutter to cut along it
	erode   int     // erosion radius (analysis pixels) breaking thin bridges
	minArea float64 // smallest panel, fraction of the page area
	leak    float64 // gutter share above which a piece's art is gutter-coloured
}

// Validate reports option values out of range.
func (o Options) Validate() error {
	switch strings.ToLower(strings.TrimSpace(o.Sensitivity)) {
	case "", SensitivityLow, SensitivityNormal, SensitivityHigh:
	default:
		return fmt.Errorf("panel detection: unknown sensitivity %q (want low, normal or high)", o.Sensitivity)
	}
	if o.GutterTolerance < 0 || o.GutterTolerance > 128 {
		return fmt.Errorf("panel detection: gutter tolerance %d out of range 0..128", o.GutterTolerance)
	}
	if o.MinPanelArea < 0 || o.MinPanelArea > 0.5 {
		return fmt.Errorf("panel detection: min panel area %g out of range 0..0.5", o.MinPanelArea)
	}
	return nil
}

func (o Options) params() params {
	var p params
	switch strings.ToLower(strings.TrimSpace(o.Sensitivity)) {
	case SensitivityLow:
		p = params{tol: 32, band: 0.95, erode: 1, minArea: 0.02, leak: 0.15}
	case SensitivityHigh:
		p = params{tol: 48, band: 0.75, erode: 3, minArea: 0.008, leak: 0.35}
	default:
		p = params{tol: 40, band: 0.85, erode: 2, minArea: 0.012, leak: 0.25}
	}
	if o.GutterTolerance > 0 {
		p.tol = min(o.GutterTolerance, 128)
	}
	if o.MinPanelArea > 0 {
		p.minArea = min(o.MinPanelArea, 0.5)
	}
	return p
}

// CacheVersion identifies the algorithm together with the effective options,
// for caching detection results: Version*1000 plus a hash of the parameters
// in 0..999 (Options that mean the same, e.g. "" and "normal", share it).
func (o Options) CacheVersion() int {
	p := o.params()
	h := fnv.New32a()
	fmt.Fprintf(h, "%d|%g|%d|%g|%g", p.tol, p.band, p.erode, p.minArea, p.leak)
	return Version*1000 + int(h.Sum32()%1000)
}

// Rect is a panel in normalized page coordinates (0..1, origin top-left).
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// FullPage is the single panel returned when no panel layout is found.
var FullPage = Rect{X: 0, Y: 0, W: 1, H: 1}

// DetectBytes decodes an encoded page image (JPEG/PNG/GIF/WebP/BMP) and
// returns its panels, see Detect.
func DetectBytes(raw []byte, rtl bool, opt Options) ([]Rect, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("panels: decoding image config: %w", err)
	}
	if cfg.Width > maxDimension || cfg.Height > maxDimension {
		return nil, fmt.Errorf("panels: image too large (%dx%d)", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("panels: decoding image: %w", err)
	}
	return Detect(img, rtl, opt), nil
}

// Detect returns the panels of a page in reading order: rows top to bottom,
// panels within a row left to right (right to left when rtl). It always
// returns at least one rectangle; a single FullPage rectangle means no usable
// panel layout was found.
func Detect(img image.Image, rtl bool, opt Options) []Rect {
	p := opt.params()
	g := newGrayPage(img, analysisSize)
	if g.w < 16 || g.h < 16 {
		return []Rect{FullPage}
	}
	bg, ok := g.gutterShade()
	if !ok {
		return []Rect{FullPage}
	}
	d := &detector{g: g, p: p, pageArea: float64(g.w * g.h), lightBG: bg >= 128}
	if opt.GutterTolerance == 0 {
		// Flat gutters get a tight tolerance, so dark art and ink next to
		// a dark gutter stay content; noisy scans get the full one.
		spread := g.frameSpread(bg, frameWidth(g.w, g.h))
		d.p.tol = min(p.tol, max(minTolerance, 6*spread+10))
	}
	d.gutter = g.floodFrom(region{0, 0, g.w, g.h}, bg, d.p.tol, frameWidth(g.w, g.h))

	regions := d.candidates()
	regions = d.cleanup(regions)

	covered := 0.0
	for _, r := range regions {
		covered += float64(r.area())
	}
	if len(regions) <= 1 || len(regions) > maxPanels || covered < minCoverage*d.pageArea {
		return []Rect{FullPage}
	}
	regions = order(regions, rtl, max(2, g.h/100))

	out := make([]Rect, len(regions))
	for i, r := range regions {
		out[i] = Rect{
			X: round4(float64(r.x0) / float64(g.w)),
			Y: round4(float64(r.y0) / float64(g.h)),
			W: round4(float64(r.x1-r.x0) / float64(g.w)),
			H: round4(float64(r.y1-r.y0) / float64(g.h)),
		}
	}
	return out
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// frameWidth is the width of the page's outer frame used for the gutter
// shade estimate and as flood-fill seeds (wide enough to step over thin
// dark scan borders).
func frameWidth(w, h int) int { return max(2, min(w, h)/60) }

// grayPage is a downscaled 8-bit grayscale copy of a page.
type grayPage struct {
	pix  []uint8
	w, h int
}

// newGrayPage converts img to grayscale, box-averaging it down so its long
// side is at most maxSide pixels (never upscaling). Common decoder outputs
// (YCbCr, Gray, RGBA, NRGBA) are read directly, without per-pixel interface
// calls.
func newGrayPage(img image.Image, maxSide int) *grayPage {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return &grayPage{}
	}
	dw, dh := sw, sh
	if long := max(sw, sh); long > maxSide {
		dw = max(1, int(math.Round(float64(sw)*float64(maxSide)/float64(long))))
		dh = max(1, int(math.Round(float64(sh)*float64(maxSide)/float64(long))))
	}

	xmap := make([]int, sw)
	for x := range sw {
		xmap[x] = x * dw / sw
	}
	sums := make([]uint32, dw*dh)
	counts := make([]uint32, dw*dh)
	row := make([]uint8, sw)
	for y := range sh {
		lumaRow(img, b, b.Min.Y+y, row)
		base := (y * dh / sh) * dw
		for x, v := range row {
			d := base + xmap[x]
			sums[d] += uint32(v)
			counts[d]++
		}
	}
	pix := make([]uint8, dw*dh)
	for i := range pix {
		pix[i] = uint8((sums[i] + counts[i]/2) / counts[i])
	}
	return &grayPage{pix: pix, w: dw, h: dh}
}

// lumaRow fills row with the gray levels of source row y.
func lumaRow(img image.Image, b image.Rectangle, y int, row []uint8) {
	switch src := img.(type) {
	case *image.YCbCr:
		off := src.YOffset(b.Min.X, y)
		copy(row, src.Y[off:off+len(row)])
	case *image.Gray:
		off := src.PixOffset(b.Min.X, y)
		copy(row, src.Pix[off:off+len(row)])
	case *image.RGBA:
		off := src.PixOffset(b.Min.X, y)
		for x := range row {
			p := src.Pix[off+4*x : off+4*x+3 : off+4*x+3]
			row[x] = luma(p[0], p[1], p[2])
		}
	case *image.NRGBA:
		off := src.PixOffset(b.Min.X, y)
		for x := range row {
			p := src.Pix[off+4*x : off+4*x+3 : off+4*x+3]
			row[x] = luma(p[0], p[1], p[2])
		}
	default:
		for x := range row {
			row[x] = color.GrayModel.Convert(img.At(b.Min.X+x, y)).(color.Gray).Y
		}
	}
}

// luma is the Rec. 601 gray level of an 8-bit RGB colour (as color.GrayModel).
func luma(r, g, b uint8) uint8 {
	return uint8((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(b) + 1<<15) >> 16)
}

// gutterShade estimates the gutter colour as the dominant gray level of the
// page's outer frame. ok is false when no shade dominates (full-bleed art).
func (g *grayPage) gutterShade() (shade uint8, ok bool) {
	return g.ringShade(region{0, 0, g.w, g.h}, frameWidth(g.w, g.h), minFrameAgreement)
}

// ringShade returns the dominant gray level of the ring of width t just
// inside r, and whether at least share of the ring agrees with it.
func (g *grayPage) ringShade(r region, t int, share float64) (uint8, bool) {
	var hist [256]int
	total := 0
	for y := r.y0; y < r.y1; y++ {
		inner := y >= r.y0+t && y < r.y1-t
		for x := r.x0; x < r.x1; x++ {
			if inner && x >= r.x0+t && x < r.x1-t {
				x = r.x1 - t - 1 // skip the inside
				continue
			}
			hist[g.pix[y*g.w+x]]++
			total++
		}
	}
	if total == 0 {
		return 0, false
	}
	// The smoothed mode: the level with the most ring pixels within ±tol.
	best, bestCount := 0, -1
	window := 0
	for k := 0; k <= frameAgreeTolerance && k < 256; k++ {
		window += hist[k]
	}
	for v := range 256 {
		if v > 0 {
			if lo := v - frameAgreeTolerance - 1; lo >= 0 {
				window -= hist[lo]
			}
			if hi := v + frameAgreeTolerance; hi < 256 {
				window += hist[hi]
			}
		}
		if window > bestCount {
			best, bestCount = v, window
		}
	}
	return uint8(best), float64(bestCount) >= share*float64(total)
}

// frameSpread is the median absolute deviation from shade of the outer
// frame's pixels near shade: about 0-1 for flat digital gutters, a few gray
// levels for scanned paper.
func (g *grayPage) frameSpread(shade uint8, t int) int {
	var hist [64]int
	n := 0
	for y := range g.h {
		inner := y >= t && y < g.h-t
		for x := 0; x < g.w; x++ {
			if inner && x >= t && x < g.w-t {
				x = g.w - t - 1
				continue
			}
			d := int(g.pix[y*g.w+x]) - int(shade)
			if d < 0 {
				d = -d
			}
			if d < len(hist) {
				hist[d]++
				n++
			}
		}
	}
	acc := 0
	for d, c := range hist {
		acc += c
		if 2*acc >= n {
			return d
		}
	}
	return len(hist)
}

// floodFrom marks (4-connected) the pixels of r within tol of shade that
// are reachable from a matching pixel in the ring of width t just inside r.
// The result covers the whole page; pixels outside r stay false.
func (g *grayPage) floodFrom(r region, shade uint8, tol, t int) []bool {
	w := g.w
	// A mid-tone gutter must not swallow the black panel borders (or white
	// balloons) next to it: never reach more than halfway to either extreme.
	s := int(shade)
	lo, hi := s-tol, s+tol
	if s > tol {
		lo = max(lo, s/2)
	}
	if 255-s > tol {
		hi = min(hi, s+(255-s)/2)
	}
	isBG := func(i int) bool {
		v := int(g.pix[i])
		return v >= lo && v <= hi
	}
	gutter := make([]bool, w*g.h)
	queue := make([]int32, 0, 4*(r.x1-r.x0+r.y1-r.y0))
	push := func(i int) {
		if !gutter[i] && isBG(i) {
			gutter[i] = true
			queue = append(queue, int32(i))
		}
	}
	for y := r.y0; y < r.y1; y++ {
		inner := y >= r.y0+t && y < r.y1-t
		for x := r.x0; x < r.x1; x++ {
			if inner && x >= r.x0+t && x < r.x1-t {
				x = r.x1 - t - 1
				continue
			}
			push(y*w + x)
		}
	}
	for len(queue) > 0 {
		i := int(queue[len(queue)-1])
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > r.x0 {
			push(i - 1)
		}
		if x < r.x1-1 {
			push(i + 1)
		}
		if y > r.y0 {
			push(i - w)
		}
		if y < r.y1-1 {
			push(i + w)
		}
	}
	return gutter
}

// region is a pixel rectangle [x0,x1)×[y0,y1) of the analysed page.
type region struct{ x0, y0, x1, y1 int }

func (r region) empty() bool { return r.x1 <= r.x0 || r.y1 <= r.y0 }
func (r region) area() int   { return max(0, r.x1-r.x0) * max(0, r.y1-r.y0) }
func (r region) w() int      { return r.x1 - r.x0 }
func (r region) h() int      { return r.y1 - r.y0 }

func (r region) union(o region) region {
	return region{min(r.x0, o.x0), min(r.y0, o.y0), max(r.x1, o.x1), max(r.y1, o.y1)}
}

func (r region) intersect(o region) region {
	return region{max(r.x0, o.x0), max(r.y0, o.y0), min(r.x1, o.x1), min(r.y1, o.y1)}
}

func (r region) overlaps(o region) bool {
	return r.x0 < o.x1 && o.x0 < r.x1 && r.y0 < o.y1 && o.y0 < r.y1
}

// detector holds the state of one page analysis.
type detector struct {
	g        *grayPage
	p        params
	lightBG  bool   // light gutters: panel borders are dark lines
	gutter   []bool // page gutter mask
	eroded   []bool // eroded content mask
	content  *mask  // summed-area table of eroded
	dark     *mask  // summed-area table of dark (border-ish) pixels, light pages
	pageArea float64
}

// candidates finds the panel regions, see analyse.
func (d *detector) candidates() []region {
	w, h := d.g.w, d.g.h
	content := make([]bool, w*h)
	for i, gut := range d.gutter {
		content[i] = !gut
	}
	d.eroded = erode(content, w, h, d.p.erode)
	d.content = newMaskFrom(d.eroded, w, region{0, 0, w, h}, true)
	if d.lightBG {
		d.dark = newDarkMask(d.g)
	}
	return d.analyse(region{0, 0, w, h}, 0)
}

// analyse splits r into panels: an XY-cut along the gutter bands of the
// eroded content; each resulting leaf is split into its connected
// components when they look like panels, else along the dark border lines
// of panels drawn touching each other (analysed again, recursively), else
// along inner gutters of another colour (innerSplit).
func (d *detector) analyse(r region, depth int) []region {
	w, h := d.g.w, d.g.h
	e := d.p.erode
	restore := func(pr region) region { // what the erosion took off the borders
		return region{max(0, pr.x0-e), max(0, pr.y0-e), min(w, pr.x1+e), min(h, pr.y1+e)}
	}
	var out []region
	for _, leaf := range d.content.segment(r, true, min(depth, 1), d.p.band, d.p.leak, d.minGap()) {
		if parts := d.componentSplit(d.eroded, leaf); len(parts) > 1 {
			for _, pr := range parts {
				out = append(out, restore(pr))
			}
			continue
		}
		if depth < maxLineDepth {
			if pieces := d.lineSplit(leaf); len(pieces) > 1 {
				for _, pc := range pieces {
					out = append(out, d.analyse(pc, depth+1)...)
				}
				continue
			}
		}
		out = append(out, d.innerSplit(restore(leaf))...)
	}
	return out
}

// newDarkMask marks the dark pixels of a page, dilated by one pixel so a
// slightly slanted or anti-aliased border line still reads as continuous.
func newDarkMask(g *grayPage) *mask {
	w, h := g.w, g.h
	return newMask(w, h, func(x, y int) bool {
		for yy := max(0, y-1); yy <= min(h-1, y+1); yy++ {
			for xx := max(0, x-1); xx <= min(w-1, x+1); xx++ {
				if g.pix[yy*w+xx] < darkLevel {
					return true
				}
			}
		}
		return false
	})
}

// lineSplit cuts r along the border lines of panels drawn touching each
// other, with no gutter between them: a thin line, dark along nearly all of
// r, with lines a few pixels to either side that are not (that rules out
// dark art). Only for light gutters, where borders are dark. Horizontal
// lines first, vertical ones when there are none.
func (d *detector) lineSplit(r region) []region {
	if d.dark == nil || r.w() < 64 || r.h() < 64 {
		return nil
	}
	for _, horizontal := range []bool{true, false} {
		lo, hi, along := r.y0, r.y1, r.w()
		frac := func(i int) float64 { return float64(d.dark.count(r.x0, i, r.x1, i+1)) / float64(along) }
		if !horizontal {
			lo, hi, along = r.x0, r.x1, r.h()
			frac = func(i int) float64 { return float64(d.dark.count(i, r.y0, i+1, r.y1)) / float64(along) }
		}
		// Caption boxes drawn across the top of a panel are thinner.
		minPiece := max((hi-lo)/6, max(d.g.w, d.g.h)/14)
		off := max(4, max(d.g.w, d.g.h)/150) // where the sides of a thin line are
		var cuts []int
		for i := lo + minPiece; i < hi-minPiece; i++ {
			if frac(i) < lineDark {
				continue
			}
			j := i
			for j < hi-minPiece && frac(j) >= lineDark {
				j++
			}
			if j-i <= off && frac(i-off) < lineSide && frac(j-1+off) < lineSide &&
				(len(cuts) == 0 || i-cuts[len(cuts)-1] >= minPiece) {
				cuts = append(cuts, (i+j)/2)
			}
			i = j
		}
		if len(cuts) == 0 {
			continue
		}
		var out []region
		start := lo
		for _, c := range cuts {
			out = append(out, sub(r, horizontal, start, c))
			start = c + 1
		}
		return append(out, sub(r, horizontal, start, hi))
	}
	return nil
}

// componentSplit handles layouts no straight cut separates (overlapping or
// L-shaped panels): the connected components of the eroded content within
// leaf, each XY-cut along its own gutters. It returns nil unless every
// sizeable component is solid like a panel — fragments of art cut out by a
// gutter colour that also occurs in the art (black gutters, black ink) mean
// the leaf is better left whole.
func (d *detector) componentSplit(eroded []bool, leaf region) []region {
	w := d.g.w
	labels, boxes := labelIn(eroded, w, leaf)
	minArea := d.p.minArea * d.pageArea
	counts := make([]int, len(boxes))
	for _, id := range labels {
		if id > 0 {
			counts[id-1]++
		}
	}
	var big []int
	for i, b := range boxes {
		if float64(b.area()) < minArea {
			continue
		}
		if float64(counts[i]) < minSolidity*float64(b.area()) {
			return nil
		}
		big = append(big, i)
	}
	if len(big) < 2 {
		return nil
	}
	var out []region
	lw := leaf.w()
	for _, i := range big {
		b := boxes[i] // leaf-local
		id := int32(i + 1)
		m := newMask(b.w(), b.h(), func(x, y int) bool {
			return labels[(y+b.y0)*lw+x+b.x0] == id
		})
		for _, pr := range m.segment(region{0, 0, b.w(), b.h()}, true, 0, d.p.band, d.p.leak, d.minGap()) {
			out = append(out, region{pr.x0 + b.x0 + leaf.x0, pr.y0 + b.y0 + leaf.y0, pr.x1 + b.x0 + leaf.x0, pr.y1 + b.y0 + leaf.y0})
		}
	}
	return out
}

// minGap is the thinnest gutter band, in analysis pixels, that splits.
func (d *detector) minGap() int { return max(2, max(d.g.w, d.g.h)/400) }

// innerSplit gives a single-panel candidate a second chance: when the ring
// just inside it has a dominant shade that is not the page's gutter colour
// (a white-guttered group of panels framed on a dark page), the pixels of
// that shade flood-filled from the ring are its inner gutters.
func (d *detector) innerSplit(r region) []region {
	g := d.g
	if float64(r.area()) < 4*d.p.minArea*d.pageArea || r.w() < 32 || r.h() < 32 {
		return []region{r}
	}
	t := max(2, min(r.w(), r.h())/80)
	shade, ok := g.ringShade(r, t, 0.6)
	if !ok {
		return []region{r}
	}
	inner := g.floodFrom(r, shade, d.p.tol/2, t)
	n := 0
	for y := r.y0; y < r.y1; y++ {
		for x := r.x0; x < r.x1; x++ {
			if !inner[y*g.w+x] {
				n++
			}
		}
	}
	// A dark or light panel border flooding most of the panel is art, not
	// gutters.
	if float64(n) < 0.6*float64(r.area()) {
		return []region{r}
	}
	m := newMaskFrom(inner, g.w, r, false)
	parts := m.segment(region{0, 0, r.w(), r.h()}, true, 0, max(d.p.band, 0.95), d.p.leak, d.minGap())
	if len(parts) < 2 {
		return []region{r}
	}
	out := make([]region, 0, len(parts))
	for _, pr := range parts {
		out = append(out, region{pr.x0 + r.x0, pr.y0 + r.y0, pr.x1 + r.x0, pr.y1 + r.y0})
	}
	return out
}

// erode keeps the pixels whose whole (2r+1)² neighbourhood (clipped to the
// page) is set, via a summed-area table.
func erode(src []bool, w, h, r int) []bool {
	if r <= 0 {
		return src
	}
	W := w + 1
	sum := make([]int32, W*(h+1))
	for y := range h {
		var rowSum int32
		for x := range w {
			if src[y*w+x] {
				rowSum++
			}
			sum[(y+1)*W+x+1] = sum[y*W+x+1] + rowSum
		}
	}
	out := make([]bool, w*h)
	for y := range h {
		y0, y1 := max(0, y-r), min(h, y+r+1)
		for x := range w {
			if !src[y*w+x] {
				continue
			}
			x0, x1 := max(0, x-r), min(w, x+r+1)
			n := sum[y1*W+x1] - sum[y0*W+x1] - sum[y1*W+x0] + sum[y0*W+x0]
			out[y*w+x] = int(n) == (x1-x0)*(y1-y0)
		}
	}
	return out
}

// labelIn numbers the 4-connected components of the set pixels of the page
// mask src within r (1..n) and returns the r-local label array and the
// components' r-local bounding boxes (index id-1).
func labelIn(src []bool, pageW int, r region) ([]int32, []region) {
	w, h := r.w(), r.h()
	set := make([]bool, w*h)
	for y := range h {
		copy(set[y*w:(y+1)*w], src[(y+r.y0)*pageW+r.x0:(y+r.y0)*pageW+r.x1])
	}
	return label(set, w, h)
}

// label numbers the 4-connected components of the set pixels (1..n) and
// returns their bounding boxes (index id-1).
func label(src []bool, w, h int) ([]int32, []region) {
	labels := make([]int32, w*h)
	var boxes []region
	var queue []int32
	for start, set := range src {
		if !set || labels[start] != 0 {
			continue
		}
		id := int32(len(boxes) + 1)
		b := region{w, h, 0, 0}
		labels[start] = id
		queue = append(queue[:0], int32(start))
		for len(queue) > 0 {
			i := int(queue[len(queue)-1])
			queue = queue[:len(queue)-1]
			x, y := i%w, i/w
			b.x0, b.y0 = min(b.x0, x), min(b.y0, y)
			b.x1, b.y1 = max(b.x1, x+1), max(b.y1, y+1)
			visit := func(j int) {
				if src[j] && labels[j] == 0 {
					labels[j] = id
					queue = append(queue, int32(j))
				}
			}
			if x > 0 {
				visit(i - 1)
			}
			if x < w-1 {
				visit(i + 1)
			}
			if y > 0 {
				visit(i - w)
			}
			if y < h-1 {
				visit(i + w)
			}
		}
		boxes = append(boxes, b)
	}
	return labels, boxes
}

// mask is a summed-area table of content pixels over a local w×h window,
// for O(1) counts.
type mask struct {
	w, h int
	sum  []int32 // (w+1)*(h+1)
}

func newMask(w, h int, isContent func(x, y int) bool) *mask {
	m := &mask{w: w, h: h, sum: make([]int32, (w+1)*(h+1))}
	W := w + 1
	for y := range h {
		var rowSum int32
		for x := range w {
			if isContent(x, y) {
				rowSum++
			}
			m.sum[(y+1)*W+x+1] = m.sum[y*W+x+1] + rowSum
		}
	}
	return m
}

// newMaskFrom is the mask of the pixels within r of the page mask src that
// equal content (src marks content when true, gutter when false).
func newMaskFrom(src []bool, pageW int, r region, content bool) *mask {
	return newMask(r.w(), r.h(), func(x, y int) bool {
		return src[(y+r.y0)*pageW+x+r.x0] == content
	})
}

// count returns the number of content pixels in [x0,x1)×[y0,y1).
func (m *mask) count(x0, y0, x1, y1 int) int {
	W := m.w + 1
	return int(m.sum[y1*W+x1] - m.sum[y0*W+x1] - m.sum[y1*W+x0] + m.sum[y0*W+x0])
}

// rowGutter reports whether at least frac of row y of r is gutter.
func (m *mask) rowGutter(r region, y int, frac float64) bool {
	n := r.x1 - r.x0
	return float64(n-m.count(r.x0, y, r.x1, y+1)) >= frac*float64(n)
}

// colGutter reports whether at least frac of column x of r is gutter.
func (m *mask) colGutter(r region, x int, frac float64) bool {
	n := r.y1 - r.y0
	return float64(n-m.count(x, r.y0, x+1, r.y1)) >= frac*float64(n)
}

// trim shrinks r to its content: gutter rows/columns on its edges go.
func (m *mask) trim(r region) region {
	for r.y0 < r.y1 && m.rowGutter(r, r.y0, trimFraction) {
		r.y0++
	}
	for r.y1 > r.y0 && m.rowGutter(r, r.y1-1, trimFraction) {
		r.y1--
	}
	for r.x0 < r.x1 && m.colGutter(r, r.x0, trimFraction) {
		r.x0++
	}
	for r.x1 > r.x0 && m.colGutter(r, r.x1-1, trimFraction) {
		r.x1--
	}
	return r
}

// split cuts a trimmed region along its gutter bands — runs of at least gap
// lines that are at least frac gutter: horizontal bands into strips (top to
// bottom) or vertical bands into columns (left to right). Each cut must leave
// real content on both sides. Returns nil when there is no band to cut along.
//
// With edges, a band must also be bounded by straight panel edges: a line
// that is mostly content within a few lines on both sides (a stripe of
// gutter-coloured art has ragged sides instead).
func (m *mask) split(r region, horizontal bool, frac float64, gap int, edges bool) []region {
	lo, hi := r.x0, r.x1
	isGutter := func(i int) bool { return m.colGutter(r, i, frac) }
	if horizontal {
		lo, hi = r.y0, r.y1
		isGutter = func(i int) bool { return m.rowGutter(r, i, frac) }
	}
	// edge reports whether, along most of the band's side, content starts
	// within edgeSearch lines of the band: lines [a,b) next to it.
	edge := func(a, b int) bool {
		a, b = max(a, lo), min(b, hi)
		if a >= b {
			return false
		}
		n, along := 0, 0
		if horizontal {
			for x := r.x0; x < r.x1; x++ {
				if m.count(x, a, x+1, b) > 0 {
					n++
				}
			}
			along = r.w()
		} else {
			for y := r.y0; y < r.y1; y++ {
				if m.count(a, y, b, y+1) > 0 {
					n++
				}
			}
			along = r.h()
		}
		return float64(n) >= edgeContent*float64(along)
	}
	// The thinnest piece worth cutting off: thinner content next to a band
	// is a balloon or caption overlapping the gutter, not a panel.
	minPiece := max(gap*3, (hi-lo)/12)
	var parts []region
	start := lo
	for i := lo; i < hi; {
		if !isGutter(i) {
			i++
			continue
		}
		j := i
		for j < hi && isGutter(j) {
			j++
		}
		if j-i >= gap && i-start >= minPiece && hi-j >= minPiece &&
			(!edges || edge(i-edgeSearch, i) && edge(j, j+edgeSearch)) {
			parts = append(parts, sub(r, horizontal, start, i))
			start = j
		}
		i = j
	}
	if len(parts) == 0 {
		return nil
	}
	return append(parts, sub(r, horizontal, start, hi))
}

func sub(r region, horizontal bool, a, b int) region {
	if horizontal {
		return region{r.x0, a, r.x1, b}
	}
	return region{a, r.y0, b, r.y1}
}

// segment is the recursive XY-cut: trim, cut in one direction (or the other
// when the first finds no band), recurse into the pieces alternating. Bands
// need only be frac gutter, except inside a piece (below the top level) that
// is more than leak gutter itself: there the gutter colour also fills the
// art (black gutters next to black ink) and only clean bands (trimFraction)
// are trusted.
func (m *mask) segment(r region, horizontal bool, depth int, frac, leak float64, gap int) []region {
	r = m.trim(r)
	if r.empty() {
		return nil
	}
	if depth >= maxDepth {
		return []region{r}
	}
	f, leaky := frac, false
	if depth > 0 && float64(r.area()-m.count(r.x0, r.y0, r.x1, r.y1)) > leak*float64(r.area()) {
		f, leaky = max(f, trimFraction), true
	}
	parts := m.split(r, horizontal, f, gap, leaky)
	if parts == nil {
		horizontal = !horizontal
		if parts = m.split(r, horizontal, f, gap, leaky); parts == nil {
			return []region{r}
		}
	}
	var out []region
	for _, p := range parts {
		out = append(out, m.segment(p, !horizontal, depth+1, frac, leak, gap)...)
	}
	return out
}

// cleanup merges boxes that are (mostly) inside another or overlap heavily,
// drops specks, and merges slivers into a neighbour when their union does not
// run into any other panel (slivers that can't be merged stay as they are).
func (d *detector) cleanup(regions []region) []region {
	minArea := d.p.minArea * d.pageArea
	// Merge contained / heavily overlapping boxes, small ones first.
	sort.SliceStable(regions, func(i, j int) bool { return regions[i].area() < regions[j].area() })
	for changed := true; changed; {
		changed = false
	outer:
		for i := 0; i < len(regions); i++ {
			for j := 0; j < len(regions); j++ {
				if i == j {
					continue
				}
				a, b := regions[i], regions[j]
				inter := a.intersect(b).area()
				small := min(a.area(), b.area())
				if small == 0 || float64(inter) < 0.5*float64(small) {
					continue
				}
				regions[j] = a.union(b)
				regions = append(regions[:i], regions[i+1:]...)
				changed = true
				break outer
			}
		}
	}

	var kept []region
	for _, r := range regions {
		if float64(r.area()) >= minArea {
			kept = append(kept, r)
		}
	}
	kept = order(kept, false, max(2, d.g.h/100))

	isSliver := func(r region) bool {
		return float64(r.w()) < sliverFraction*float64(d.g.w) ||
			float64(r.h()) < sliverFraction*float64(d.g.h)
	}
	for changed := true; changed; {
		changed = false
		for i, r := range kept {
			if !isSliver(r) {
				continue
			}
			best := -1
			for _, j := range []int{i + 1, i - 1} { // prefer the next panel: captions lead into it
				if j < 0 || j >= len(kept) {
					continue
				}
				u := r.union(kept[j])
				clash := false
				for k, o := range kept {
					if k != i && k != j && u.overlaps(o) {
						clash = true
						break
					}
				}
				if !clash {
					best = j
					break
				}
			}
			if best < 0 {
				continue
			}
			kept[best] = r.union(kept[best])
			kept = append(kept[:i], kept[i+1:]...)
			changed = true
			break
		}
	}
	return kept
}

// order sorts panels into reading order with an XY-cut over the boxes:
// split into rows along horizontal lines no box crosses (by more than tol),
// rows into columns along vertical lines, recursively; columns run right to
// left when rtl. Boxes no line separates stay sorted top to bottom, then
// left to right.
func order(rs []region, rtl bool, tol int) []region {
	if len(rs) <= 1 {
		return rs
	}
	if groups := cutGroups(rs, true, tol); len(groups) > 1 {
		var out []region
		for _, g := range groups {
			out = append(out, order(g, rtl, tol)...)
		}
		return out
	}
	if groups := cutGroups(rs, false, tol); len(groups) > 1 {
		if rtl {
			for a, b := 0, len(groups)-1; a < b; a, b = a+1, b-1 {
				groups[a], groups[b] = groups[b], groups[a]
			}
		}
		var out []region
		for _, g := range groups {
			out = append(out, order(g, rtl, tol)...)
		}
		return out
	}
	out := append([]region(nil), rs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.y0 != b.y0 {
			return a.y0 < b.y0
		}
		if rtl {
			return a.x1 > b.x1
		}
		return a.x0 < b.x0
	})
	return out
}

// cutGroups partitions boxes along horizontal (or vertical) lines that no
// box crosses by more than tol, sorted top to bottom (left to right).
func cutGroups(rs []region, horizontal bool, tol int) [][]region {
	span := func(r region) (int, int) {
		if horizontal {
			return r.y0, r.y1
		}
		return r.x0, r.x1
	}
	sorted := append([]region(nil), rs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, _ := span(sorted[i])
		b, _ := span(sorted[j])
		return a < b
	})
	var groups [][]region
	var cur []region
	end := math.MinInt
	for _, r := range sorted {
		a, b := span(r)
		if len(cur) > 0 && a >= end-tol {
			groups = append(groups, cur)
			cur = nil
		}
		cur = append(cur, r)
		if len(cur) == 1 {
			end = b
		} else {
			end = max(end, b)
		}
	}
	return append(groups, cur)
}
