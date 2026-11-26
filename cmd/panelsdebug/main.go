// Command panelsdebug is a developer tool for the guided view's panel
// detector (internal/panels): it runs detection on pages of comic archives
// and writes overlay PNGs, prints the rectangles as JSON, or scores the
// detector against a hand-made ground-truth file.
//
//	go run ./cmd/panelsdebug -out /tmp/panels -sample 20 -pages 4 /comics
//	go run ./cmd/panelsdebug -out /tmp/panels -range 3-8 "/comics/Saga 001.cbz"
//	go run ./cmd/panelsdebug -eval truth.json -out /tmp/eval
//
// Paths may be archives (CBZ/CBR) or folders, searched recursively. The
// ground-truth file is JSON:
//
//	{"pages":[{"archive":"/comics/Saga 001.cbz","page":5,"rtl":false,
//	           "panels":[{"x":0.05,"y":0.04,"w":0.9,"h":0.3}, ...]}]}
//
// with panels in normalized page coordinates (0..1) in reading order. A
// detected panel matches a true one when their intersection over union is at
// least 0.5 (each panel matches at most once).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"comicnest/internal/library"
	"comicnest/internal/panels"
)

func main() {
	out := flag.String("out", "", "directory for overlay PNGs (none when empty)")
	sample := flag.Int("sample", 0, "use only N archives, spread evenly over the sorted list (0 = all)")
	perIssue := flag.Int("pages", 4, "interior pages per archive, spread evenly (covers and the last page skipped)")
	pageRange := flag.String("range", "", "explicit 0-based page range a-b (or a single page), overrides -pages")
	height := flag.Int("height", 1000, "height of the overlay PNGs")
	asJSON := flag.Bool("json", false, "print the detected rectangles as JSON lines")
	rtl := flag.Bool("rtl", false, "right-to-left (manga) reading order")
	sens := flag.String("sensitivity", "", "detection sensitivity: low, normal (default) or high")
	gutterTol := flag.Int("gutter-tolerance", 0, "advanced: gutter colour tolerance in gray levels (0 = default)")
	minPanel := flag.Float64("min-panel", 0, "advanced: smallest panel as a fraction of the page area (0 = default)")
	evalPath := flag.String("eval", "", "score detection against this ground-truth JSON file")
	template := flag.String("truth-template", "", "write a ground-truth template (detected rects) for the processed pages to this file")
	grid := flag.Bool("grid", false, "draw a 10% grid with labels on the overlays (handy for annotating)")
	list := flag.Bool("list", false, "only list the archives found")
	crop := flag.String("crop", "", "write only this normalized part x0,y0,x1,y1 of each overlay (zoom in)")
	flag.Parse()

	opts := panels.Options{Sensitivity: *sens, GutterTolerance: *gutterTol, MinPanelArea: *minPanel}
	if err := opts.Validate(); err != nil {
		log.Fatal(err)
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	r := &runner{opts: opts, out: *out, height: *height, grid: *grid, crop: *crop}

	if *evalPath != "" {
		if err := r.eval(*evalPath); err != nil {
			log.Fatal(err)
		}
		return
	}

	archives := findArchives(flag.Args())
	if *sample > 0 && *sample < len(archives) {
		picked := make([]string, 0, *sample)
		for i := range *sample {
			picked = append(picked, archives[i*len(archives) / *sample])
		}
		archives = picked
	}
	if *list {
		for _, a := range archives {
			fmt.Println(a)
		}
		return
	}

	var truth truthFile
	var total time.Duration
	n := 0
	for _, a := range archives {
		names, err := library.ListPages(a)
		if err != nil {
			log.Printf("%s: %v", a, err)
			continue
		}
		for _, p := range choosePages(len(names), *perIssue, *pageRange) {
			raw, _, err := library.ExtractPage(a, p)
			if err != nil {
				log.Printf("%s page %d: %v", a, p, err)
				continue
			}
			res, err := r.detect(raw, *rtl)
			if err != nil {
				log.Printf("%s page %d: %v", a, p, err)
				continue
			}
			total += res.elapsed
			n++
			id := pageID(a, p)
			if *asJSON {
				b, _ := json.Marshal(struct {
					ID      string        `json:"id"`
					Archive string        `json:"archive"`
					Page    int           `json:"page"`
					Panels  []panels.Rect `json:"panels"`
				}{id, a, p, res.rects})
				fmt.Println(string(b))
			} else {
				fmt.Printf("%-60s %3d panels  %v\n", id, len(res.rects), res.elapsed.Round(time.Millisecond))
			}
			if err := r.writeOverlay(id, res, nil); err != nil {
				log.Print(err)
			}
			truth.Pages = append(truth.Pages, truthPage{ID: id, Archive: a, Page: p, RTL: *rtl, Panels: res.rects})
		}
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "%d pages, average decode+detect %v\n", n, (total / time.Duration(n)).Round(time.Millisecond))
	}
	if *template != "" {
		b, _ := json.MarshalIndent(truth, "", "  ")
		if err := os.WriteFile(*template, b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

// findArchives expands folders into the CBZ/CBR archives below them, sorted.
func findArchives(args []string) []string {
	var out []string
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			log.Print(err)
			continue
		}
		if !fi.IsDir() {
			out = append(out, a)
			continue
		}
		filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				log.Print(err)
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".cbz", ".cbr", ".zip", ".rar":
				if !d.IsDir() {
					out = append(out, p)
				}
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// choosePages picks k interior pages of an n-page archive evenly, skipping
// the cover (and the page after it, often a recap/credits page) and the last
// page, or parses an explicit range.
func choosePages(n, k int, rng string) []int {
	if rng != "" {
		a, b, found := strings.Cut(rng, "-")
		lo, err1 := strconv.Atoi(a)
		hi := lo
		var err2 error
		if found {
			hi, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil {
			log.Fatalf("bad -range %q", rng)
		}
		var out []int
		for p := max(lo, 0); p <= hi && p < n; p++ {
			out = append(out, p)
		}
		return out
	}
	first, last := 2, n-2
	if last < first {
		first, last = 0, n-1
	}
	span := last - first + 1
	if span <= 0 {
		return nil
	}
	if k > span {
		k = span
	}
	var out []int
	for i := range k {
		out = append(out, first+(2*i+1)*span/(2*k))
	}
	return out
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// pageID is a short file-name-safe identifier of an archive page.
func pageID(archive string, page int) string {
	base := strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive))
	base = strings.Trim(unsafeChars.ReplaceAllString(base, "_"), "_")
	if len(base) > 48 {
		base = base[:48]
	}
	return fmt.Sprintf("%s_p%03d", base, page)
}

type runner struct {
	opts   panels.Options
	out    string
	height int
	grid   bool
	crop   string
}

type result struct {
	img     image.Image
	rects   []panels.Rect
	elapsed time.Duration
}

// detect decodes a page and runs detection, timing decode+detect like the
// server does.
func (r *runner) detect(raw []byte, rtl bool) (result, error) {
	start := time.Now()
	rects, err := panels.DetectBytes(raw, rtl, r.opts)
	elapsed := time.Since(start)
	if err != nil {
		return result{}, err
	}
	img, _, err := image.Decode(strings.NewReader(string(raw)))
	if err != nil {
		return result{}, err
	}
	return result{img: img, rects: rects, elapsed: elapsed}, nil
}

var palette = []color.RGBA{
	{230, 25, 75, 255}, {60, 180, 75, 255}, {0, 130, 200, 255}, {245, 130, 48, 255},
	{145, 30, 180, 255}, {70, 240, 240, 255}, {240, 50, 230, 255}, {210, 245, 60, 255},
	{250, 190, 212, 255}, {0, 128, 128, 255}, {170, 110, 40, 255}, {128, 0, 0, 255},
}

// writeOverlay saves the page scaled to r.height with the detected panels as
// thick numbered outlines and, when given, the true panels as thin dashed
// green outlines.
func (r *runner) writeOverlay(id string, res result, truth []panels.Rect) error {
	if r.out == "" {
		return nil
	}
	b := res.img.Bounds()
	h := r.height
	w := max(1, b.Dx()*h/b.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), res.img, b, draw.Src, nil)
	// Fade the page so the outlines stand out.
	for i := 0; i < len(dst.Pix); i += 4 {
		for c := range 3 {
			dst.Pix[i+c] = uint8(int(dst.Pix[i+c])*3/4 + 64)
		}
	}
	if r.grid {
		gc := color.RGBA{0, 160, 255, 255}
		for k := 1; k < 10; k++ {
			x, y := k*w/10, k*h/10
			for t := 0; t < h; t += 2 {
				dst.SetRGBA(x, t, gc)
			}
			for t := 0; t < w; t += 2 {
				dst.SetRGBA(t, y, gc)
			}
			drawNumber(dst, x+2, 2, k, 2, gc)
			drawNumber(dst, 2, y+2, k, 2, gc)
		}
	}
	px := func(p panels.Rect) image.Rectangle {
		return image.Rect(int(p.X*float64(w)), int(p.Y*float64(h)),
			int((p.X+p.W)*float64(w)), int((p.Y+p.H)*float64(h)))
	}
	green := color.RGBA{0, 200, 0, 255}
	for _, t := range truth {
		outline(dst, px(t).Inset(-3), 2, green, true)
	}
	for i, p := range res.rects {
		c := palette[i%len(palette)]
		pr := px(p)
		outline(dst, pr, 5, c, false)
		scale := max(3, h/120)
		bx := pr.Min.X + 8
		by := pr.Min.Y + 8
		digits := len(strconv.Itoa(i + 1))
		box := image.Rect(bx-3, by-3, bx+digits*4*scale+2, by+5*scale+3)
		draw.Draw(dst, box, &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		drawNumber(dst, bx, by, i+1, scale, c)
	}
	var outImg image.Image = dst
	if r.crop != "" {
		var c [4]float64
		if _, err := fmt.Sscanf(r.crop, "%g,%g,%g,%g", &c[0], &c[1], &c[2], &c[3]); err != nil {
			return fmt.Errorf("bad -crop %q (want x0,y0,x1,y1)", r.crop)
		}
		outImg = dst.SubImage(image.Rect(int(c[0]*float64(w)), int(c[1]*float64(h)),
			int(c[2]*float64(w)), int(c[3]*float64(h))))
	}
	f, err := os.Create(filepath.Join(r.out, id+".png"))
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, outImg)
}

func outline(img *image.RGBA, r image.Rectangle, t int, c color.RGBA, dashed bool) {
	b := img.Bounds()
	set := func(x, y int) {
		if dashed && ((x+y)/8)%2 == 1 {
			return
		}
		if (image.Point{x, y}).In(b) {
			img.SetRGBA(x, y, c)
		}
	}
	for k := range t {
		for x := r.Min.X; x < r.Max.X; x++ {
			set(x, r.Min.Y+k)
			set(x, r.Max.Y-1-k)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			set(r.Min.X+k, y)
			set(r.Max.X-1-k, y)
		}
	}
}

// digitFont is a 3x5 bitmap font, one row of three bits per line.
var digitFont = [10][5]uint8{
	{7, 5, 5, 5, 7}, {2, 6, 2, 2, 7}, {7, 1, 7, 4, 7}, {7, 1, 7, 1, 7}, {5, 5, 7, 1, 1},
	{7, 4, 7, 1, 7}, {7, 4, 7, 5, 7}, {7, 1, 1, 1, 1}, {7, 5, 7, 5, 7}, {7, 5, 7, 1, 7},
}

func drawNumber(img *image.RGBA, x, y, n, scale int, c color.RGBA) {
	for _, ch := range strconv.Itoa(n) {
		glyph := digitFont[ch-'0']
		for row := range 5 {
			for col := range 3 {
				if glyph[row]&(4>>col) == 0 {
					continue
				}
				r := image.Rect(x+col*scale, y+row*scale, x+(col+1)*scale, y+(row+1)*scale)
				draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
			}
		}
		x += 4 * scale
	}
}

type truthFile struct {
	Pages []truthPage `json:"pages"`
}

type truthPage struct {
	ID      string        `json:"id,omitempty"`
	Archive string        `json:"archive"`
	Page    int           `json:"page"`
	RTL     bool          `json:"rtl,omitempty"`
	Note    string        `json:"note,omitempty"`
	Panels  []panels.Rect `json:"panels,omitempty"`
	// Boxes is an alternative to Panels that is easier to write by hand:
	// [x0, y0, x1, y1] corners, normalized.
	Boxes [][4]float64 `json:"boxes,omitempty"`
}

func iou(a, b panels.Rect) float64 {
	ix := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	iy := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if ix <= 0 || iy <= 0 {
		return 0
	}
	inter := ix * iy
	return inter / (a.W*a.H + b.W*b.H - inter)
}

// match greedily pairs detected and true panels by descending IoU (≥ 0.5)
// and returns the number of pairs.
func match(det, truth []panels.Rect) int {
	type pair struct {
		d, t int
		v    float64
	}
	var pairs []pair
	for i, d := range det {
		for j, t := range truth {
			if v := iou(d, t); v >= 0.5 {
				pairs = append(pairs, pair{i, j, v})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].v > pairs[b].v })
	usedD, usedT := map[int]bool{}, map[int]bool{}
	n := 0
	for _, p := range pairs {
		if usedD[p.d] || usedT[p.t] {
			continue
		}
		usedD[p.d], usedT[p.t] = true, true
		n++
	}
	return n
}

func prf(tp, nDet, nTruth int) (p, r, f float64) {
	if nDet > 0 {
		p = float64(tp) / float64(nDet)
	}
	if nTruth > 0 {
		r = float64(tp) / float64(nTruth)
	}
	if p+r > 0 {
		f = 2 * p * r / (p + r)
	}
	return
}

func (r *runner) eval(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var tf truthFile
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // UTF-8 BOM (Windows editors)
	if err := json.Unmarshal(raw, &tf); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	type scored struct {
		id            string
		tp, det, true int
		f             float64
	}
	var all []scored
	tpSum, detSum, trueSum, exact := 0, 0, 0, 0
	var total, worst time.Duration
	for _, tp := range tf.Pages {
		for _, b := range tp.Boxes {
			tp.Panels = append(tp.Panels, panels.Rect{X: b[0], Y: b[1], W: b[2] - b[0], H: b[3] - b[1]})
		}
		data, _, err := library.ExtractPage(tp.Archive, tp.Page)
		if err != nil {
			return fmt.Errorf("%s page %d: %w", tp.Archive, tp.Page, err)
		}
		res, err := r.detect(data, tp.RTL)
		if err != nil {
			return fmt.Errorf("%s page %d: %w", tp.Archive, tp.Page, err)
		}
		total += res.elapsed
		worst = max(worst, res.elapsed)
		id := tp.ID
		if id == "" {
			id = pageID(tp.Archive, tp.Page)
		}
		m := match(res.rects, tp.Panels)
		_, _, f := prf(m, len(res.rects), len(tp.Panels))
		all = append(all, scored{id, m, len(res.rects), len(tp.Panels), f})
		tpSum += m
		detSum += len(res.rects)
		trueSum += len(tp.Panels)
		if m == len(res.rects) && m == len(tp.Panels) {
			exact++
		}
		if err := r.writeOverlay(id, res, tp.Panels); err != nil {
			log.Print(err)
		}
	}
	if len(all) == 0 {
		return fmt.Errorf("%s: no pages", path)
	}
	p, rc, f := prf(tpSum, detSum, trueSum)
	fmt.Printf("pages %d  exact %d (%.0f%%)  panels: true %d detected %d matched %d\n",
		len(all), exact, 100*float64(exact)/float64(len(all)), trueSum, detSum, tpSum)
	fmt.Printf("precision %.3f  recall %.3f  F1 %.3f\n", p, rc, f)
	fmt.Printf("time: average %v, slowest %v (decode+detect)\n",
		(total / time.Duration(len(all))).Round(time.Millisecond), worst.Round(time.Millisecond))
	sort.SliceStable(all, func(a, b int) bool { return all[a].f < all[b].f })
	fmt.Println("worst pages:")
	for i, s := range all {
		if i >= 25 || s.f == 1 {
			break
		}
		fmt.Printf("  %-56s F1 %.2f  matched %d / detected %d / true %d\n", s.id, s.f, s.tp, s.det, s.true)
	}
	return nil
}
