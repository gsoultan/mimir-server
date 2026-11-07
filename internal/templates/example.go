package templates

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Sizes of the made-up example data.
const (
	ExampleRows        = 2000 // rows of a table template
	ExamplePhotoRows   = 600  // rows of a photos-with-details template
	ExamplePhotosEach  = 60   // photos per folder of a photo template
	examplePhotoPixels = 64   // width and height of a made-up photo
)

// Example returns a template's made-up example data: a CSV, or a ZIP for
// templates that read photos. The same template always gives the same
// data. The answers follow from the other columns, so a model taught from
// it learns something, but none of it is real.
func Example(t Template) (name, contentType string, data []byte, err error) {
	h := fnv.New64a()
	h.Write([]byte(t.ID))
	g := &gen{t: t, r: rand.New(rand.NewPCG(h.Sum64(), 7))}
	switch t.Data {
	case Photos:
		data, err = g.photos()
		return t.ID + "-example.zip", "application/zip", data, err
	case PhotoTable:
		data, err = g.photoTable()
		return t.ID + "-example.zip", "application/zip", data, err
	}
	var buf bytes.Buffer
	switch t.Arch {
	case recommendArch:
		err = g.picks(&buf, ExampleRows)
	case bestActionArch:
		err = g.actions(&buf, ExampleRows)
	case nearbyArch:
		err = g.nearby(&buf, ExampleRows)
	case survivalArch:
		err = g.survival(&buf, ExampleRows)
	default:
		err = g.table(&buf, ExampleRows, nil)
	}
	if err != nil {
		return "", "", nil, err
	}
	name = t.Table
	if name == "" {
		name = t.ID
	}
	return name + "-example.csv", "text/csv", buf.Bytes(), nil
}

// The model types of Ranked templates whose examples are made their own
// way.
const (
	recommendArch  = "recommend"
	bestActionArch = "best-action"
	nearbyArch     = "recommend-nearby"
	survivalArch   = "time-to-event"
)

type gen struct {
	t Template
	r *rand.Rand
}

// answers returns the values the answer column takes in the examples.
func (g *gen) answers() []string {
	a := g.t.Answer()
	var out []string
	for _, e := range a.Examples {
		if e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if g.t.Kind == YesNo {
		yes, no := "yes", "no"
		for _, e := range out {
			if v, ok := yesNo(e); ok && v {
				yes = e
			} else if ok {
				no = e
			}
		}
		return []string{no, yes}
	}
	if len(out) < 2 {
		out = append(out, "other")
	}
	return out
}

// table writes rows of made-up data. photo, when set, names the photo of
// row i and is told its answer index.
func (g *gen) table(w *bytes.Buffer, rows int, photo func(i, answer int) string) error {
	cw := csv.NewWriter(w)
	g.header(cw)
	inputs := g.t.Inputs()
	answers := g.answers()
	// Each input column gets a hidden score per row; the answer is a
	// weighted mix of them, with some noise, so it can be learned.
	classes := len(answers)
	if g.t.Kind == Number {
		classes = 1
	}
	weights := make([][]float64, classes)
	for c := range weights {
		weights[c] = make([]float64, len(inputs))
		for j := range inputs {
			weights[c][j] = (0.5 + g.r.Float64()) * float64(1-2*g.r.IntN(2))
		}
	}
	answer := g.t.Answer()
	lo, hi, whole, positive := numberRange(answer.Examples)
	for i := range rows {
		z := make([]float64, len(inputs))
		for j := range z {
			z[j] = g.r.NormFloat64()
		}
		score := make([]float64, classes)
		for c := range score {
			for j, in := range inputs {
				if in.Kind != schema.Text && in.Kind != schema.Photo {
					score[c] += weights[c][j] * z[j]
				}
			}
			score[c] = score[c]/math.Sqrt(float64(max(1, len(inputs)))) + 0.35*g.r.NormFloat64()
		}
		var value string
		at := 0 // the answer's index, for picking text and photos
		switch g.t.Kind {
		case Number:
			v := (lo+hi)/2 + score[0]*(hi-lo)/2
			value = formatNumber(v, whole, positive)
			at = nearest(answer.Examples, v)
		case YesNo:
			if score[0] > 1.0 { // about one row in six says yes
				at = 1
			}
			value = answers[at]
		default:
			for c := range score {
				if score[c] > score[at] {
					at = c
				}
			}
			value = answers[at]
		}
		rec := make([]string, 0, len(g.t.Columns))
		for j, in := range inputs {
			rec = append(rec, g.cell(in, z[j], value, i, at, photo))
		}
		cw.Write(append(rec, value))
	}
	cw.Flush()
	return cw.Error()
}

// header writes the template's column names.
func (g *gen) header(cw *csv.Writer) {
	header := make([]string, len(g.t.Columns))
	for i, c := range g.t.Columns {
		header[i] = c.Name
	}
	cw.Write(header)
}

// picks writes made-up order lines for a recommend template, whose first
// column says who picked and whose answer is the item. Customers fall
// into a few groups of similar taste, and each works through a list of
// products of their own, mostly their group's favourites in an order of
// their own, so what a customer buys next can be told from what customers
// like them bought. A customer who reaches the end of the list orders it
// again. Some customers order far more often than others, as in real
// order books.
func (g *gen) picks(w *bytes.Buffer, rows int) error {
	const customers, products, tastes, favourites = 200, 60, 6, 12
	inputs := g.t.Inputs()
	people := names(inputs[0].Examples, "C-", customers)
	items := names(g.t.Answer().Examples, "P-", products)
	fav := make([][]int, tastes)
	for k := range fav {
		fav[k] = g.r.Perm(products)[:favourites]
	}
	lists := make([][]int, customers)
	for c := range lists {
		k := g.r.IntN(tastes)
		for _, i := range g.r.Perm(favourites) {
			lists[c] = append(lists[c], fav[k][i])
			if g.r.Float64() < 0.15 { // now and then, something else
				lists[c] = append(lists[c], g.r.IntN(products))
			}
		}
	}
	next := make([]int, customers)
	cw := csv.NewWriter(w)
	g.header(cw)
	for range rows {
		c := int(float64(customers) * math.Pow(g.r.Float64(), 1.3))
		p := lists[c][next[c]%len(lists[c])]
		next[c]++
		rec := []string{people[c]}
		for _, in := range inputs[1:] {
			// Inside the examples' range, so an amount is never zero.
			rec = append(rec, g.cell(in, 2*g.r.Float64()-1, items[p], 0, 0, nil))
		}
		cw.Write(append(rec, items[p]))
	}
	cw.Flush()
	return cw.Error()
}

// actions writes a made-up log for a best-action template, whose answer
// is the action taken and whose last input says how it turned out. Each
// row is a situation and an action picked at random, as when people try
// different steps; whether it worked depends on how well the action
// suits the situation, so the best one differs from row to row.
func (g *gen) actions(w *bytes.Buffer, rows int) error {
	inputs := g.t.Inputs()
	situation, reward := inputs[:len(inputs)-1], inputs[len(inputs)-1]
	acts := g.answers()
	weights := make([][]float64, len(acts))
	bias := make([]float64, len(acts))
	for a := range weights {
		bias[a] = 0.6 * g.r.NormFloat64()
		weights[a] = make([]float64, len(situation))
		for j := range situation {
			weights[a][j] = (0.5 + g.r.Float64()) * float64(1-2*g.r.IntN(2))
		}
	}
	lo, hi, whole, positive := numberRange(reward.Examples)
	cw := csv.NewWriter(w)
	g.header(cw)
	for i := range rows {
		a := g.r.IntN(len(acts))
		s := bias[a]
		rec := make([]string, 0, len(g.t.Columns))
		for j, in := range situation {
			z := g.r.NormFloat64()
			if in.Kind != schema.Text && in.Kind != schema.Photo {
				s += 1.5 * weights[a][j] * z / math.Sqrt(float64(max(1, len(situation))))
			}
			rec = append(rec, g.cell(in, z, acts[a], i, a, nil))
		}
		chance := 1 / (1 + math.Exp(0.8-s))
		var out string
		switch {
		case reward.Kind == schema.Number:
			out = formatNumber(lo+chance*(hi-lo), whole, positive)
		case g.r.Float64() < chance:
			out = "yes"
		default:
			out = "no"
		}
		cw.Write(append(rec, out, acts[a]))
	}
	cw.Flush()
	return cw.Error()
}

// nearby writes made-up order lines for a recommend-nearby template,
// whose inputs are the customer, its category, latitude and longitude,
// then any amounts, and whose answer is the product. Customers sit around
// the example places, a few kilometres from the centre, and buy mostly
// what their category buys plus a few products local to their place, so
// what customers of the same kind nearby buy says what a customer will
// want. The catalogue is the example products and more, up to 60.
func (g *gen) nearby(w *bytes.Buffer, rows int) error {
	inputs := g.t.Inputs()
	distinct := func(c Column) []string {
		var out []string
		for _, e := range c.Examples {
			if e != "" && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
		return out
	}
	cats := distinct(inputs[1])
	if len(cats) == 0 {
		cats = []string{"retail"}
	}
	type place struct{ lat, lon float64 }
	var places []place
	for i := range min(len(inputs[2].Examples), len(inputs[3].Examples)) {
		lat, ok1 := schema.ParseNumber(inputs[2].Examples[i])
		lon, ok2 := schema.ParseNumber(inputs[3].Examples[i])
		if ok1 && ok2 && !slices.Contains(places, place{lat, lon}) {
			places = append(places, place{lat, lon})
		}
	}
	if len(places) == 0 {
		places = []place{{0, 0}}
	}
	// The example products, and more like a real catalogue.
	products := distinct(g.t.Answer())
	products = append(products, names(nil, "SKU-", max(0, 60-len(products)))...)
	// Each category has a core of products; each place adds its own.
	core := make([][]int, len(cats))
	for k := range core {
		core[k] = g.r.Perm(len(products))[:min(5, len(products))]
	}
	local := make([][][]int, len(places))
	for c := range local {
		local[c] = make([][]int, len(cats))
		for k := range local[c] {
			local[c][k] = g.r.Perm(len(products))[:min(3, len(products))]
		}
	}
	customers := max(rows/5, 10)
	people := names(inputs[0].Examples, "C-", customers)
	cw := csv.NewWriter(w)
	g.header(cw)
	for u := range customers {
		c, k := g.r.IntN(len(places)), g.r.IntN(len(cats))
		// About 10 km around the centre.
		lat := places[c].lat + 0.06*g.r.NormFloat64()
		lon := places[c].lon + 0.06*g.r.NormFloat64()
		likes := append(slices.Clone(core[k]), local[c][k]...)
		picked := map[int]bool{}
		for range 3 + g.r.IntN(5) {
			p := likes[g.r.IntN(len(likes))]
			if g.r.Float64() < 0.1 {
				p = g.r.IntN(len(products))
			}
			if picked[p] {
				continue
			}
			picked[p] = true
			rec := []string{people[u], cats[k], strconv.FormatFloat(lat, 'f', 5, 64), strconv.FormatFloat(lon, 'f', 5, 64)}
			for _, in := range inputs[4:] {
				rec = append(rec, g.cell(in, 2*g.r.Float64()-1, products[p], 0, 0, nil))
			}
			cw.Write(append(rec, products[p]))
		}
	}
	cw.Flush()
	return cw.Error()
}

// survival writes made-up rows for a time-to-event template, whose last
// input is how many days each row was watched and whose answer says
// whether it happened. How soon it happens follows the other inputs;
// each row is watched for a while, so for some it hasn't happened yet.
func (g *gen) survival(w *bytes.Buffer, rows int) error {
	inputs := g.t.Inputs()
	situation := inputs[:len(inputs)-1]
	lo, hi, _, _ := numberRange(inputs[len(inputs)-1].Examples)
	// The usual time is a third of the way into the watch.
	typical := lo + (hi-lo)/3
	weights := make([]float64, len(situation))
	for j := range weights {
		weights[j] = (0.5 + g.r.Float64()) * float64(1-2*g.r.IntN(2))
	}
	yes, no := "yes", "no"
	cw := csv.NewWriter(w)
	g.header(cw)
	for i := range rows {
		rec := make([]string, 0, len(g.t.Columns))
		score := 0.0
		for j, in := range situation {
			z := g.r.NormFloat64()
			if in.Kind != schema.Text && in.Kind != schema.Photo {
				score += weights[j] * z / math.Sqrt(float64(max(1, len(situation))))
			}
			rec = append(rec, g.cell(in, z, "", i, 0, nil))
		}
		when := g.r.ExpFloat64() * typical * math.Exp(-1.2*score)
		watch := lo + g.r.Float64()*(hi-lo)
		out := no
		if when <= watch {
			watch, out = when, yes
		}
		cw.Write(append(rec, formatNumber(max(watch, 0.5), false, true), out))
	}
	cw.Flush()
	return cw.Error()
}

// names makes n names like the examples: their letters before the
// number, such as C- of C-1042, then 1001, 1002 and on. fallback is the
// letters when no example has any.
func names(examples []string, fallback string, n int) []string {
	prefix := fallback
	for _, e := range examples {
		if p := strings.TrimRight(e, "0123456789"); p != "" && p != e {
			prefix = p
			break
		}
	}
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strconv.Itoa(1001+i)
	}
	return out
}

// cell makes one input cell from its hidden score z.
func (g *gen) cell(c Column, z float64, answer string, row, at int, photo func(i, answer int) string) string {
	switch c.Kind {
	case schema.Number:
		lo, hi, whole, positive := numberRange(c.Examples)
		return formatNumber((lo+hi)/2+z*(hi-lo)/2, whole, positive)
	case schema.YesNo:
		if z > 0.4 {
			return "yes"
		}
		return "no"
	case schema.Category:
		var vals []string
		for _, e := range c.Examples {
			if e != "" && !slices.Contains(vals, e) {
				vals = append(vals, e)
			}
		}
		if len(vals) == 0 {
			return ""
		}
		k := int(normal(z) * float64(len(vals)))
		return vals[min(k, len(vals)-1)]
	case schema.Text:
		return g.text(c, answer, at)
	case schema.Photo:
		if photo != nil {
			return photo(row, at)
		}
	}
	return ""
}

// text makes a message for a row: mostly the example written next to the
// same answer, mixed with words from the others, so the words hint at the
// answer without giving it away every time.
func (g *gen) text(c Column, answer string, at int) string {
	var same, all []string
	ans := g.t.Answer().Examples
	for k, e := range c.Examples {
		if e == "" {
			continue
		}
		all = append(all, e)
		if g.t.Kind == Number && k == at || g.t.Kind != Number && k < len(ans) && ans[k] == answer {
			same = append(same, e)
		}
	}
	if len(all) == 0 {
		return ""
	}
	base := all[g.r.IntN(len(all))]
	if len(same) > 0 && g.r.Float64() < 0.8 {
		base = same[g.r.IntN(len(same))]
	}
	filler := []string{"please", "today", "again", "thanks", "this week", "as soon as possible", "for the second time", "on site"}
	if g.r.Float64() < 0.5 {
		base += ", " + filler[g.r.IntN(len(filler))]
	}
	return base
}

// photos makes a ZIP of photo folders.
func (g *gen) photos() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for c, f := range g.t.Folders {
		for i := range ExamplePhotosEach {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%s-example/%s/%s-%03d.jpg", g.t.ID, f.Name, f.Name, i+1), Method: zip.Store})
			if err != nil {
				return nil, err
			}
			if err := jpeg.Encode(w, g.picture(c, len(g.t.Folders)), &jpeg.Options{Quality: 85}); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// photoTable makes a ZIP of a data.csv and the photos it names.
func (g *gen) photoTable() ([]byte, error) {
	type pic struct {
		name   string
		answer int
	}
	var pics []pic
	prefix := "photo"
	for _, c := range g.t.Columns {
		if c.Kind == schema.Photo && len(c.Examples) > 0 {
			base := c.Examples[0]
			base = base[strings.LastIndex(base, "/")+1:]
			prefix = strings.TrimRight(strings.TrimSuffix(base, ".jpg"), "0123456789")
		}
	}
	var table bytes.Buffer
	err := g.table(&table, ExamplePhotoRows, func(i, answer int) string {
		name := fmt.Sprintf("%s%05d.jpg", prefix, i+1)
		pics = append(pics, pic{name, answer})
		return "photos/" + name
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("data.csv")
	if err != nil {
		return nil, err
	}
	w.Write(table.Bytes())
	classes := len(g.answers())
	for _, p := range pics {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "photos/" + p.name, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if err := jpeg.Encode(w, g.picture(p.answer, classes), &jpeg.Options{Quality: 85}); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// picture draws a made-up photo for answer c of n: a background and a
// shape whose colour and form depend on the answer, at a random place, so
// each answer looks different but no two photos are the same.
func (g *gen) picture(c, n int) image.Image {
	const side = examplePhotoPixels
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	hue := float64(c) / float64(max(n, 1))
	bg := hsv(hue+0.5, 0.15, 0.9)
	fg := hsv(hue, 0.7, 0.55)
	cx, cy := side/2+g.r.IntN(17)-8, side/2+g.r.IntN(17)-8
	rad := float64(12 + g.r.IntN(8))
	shape := c % 3
	for y := range side {
		for x := range side {
			dx, dy := float64(x-cx), float64(y-cy)
			var in bool
			switch shape {
			case 0: // a round spot
				in = dx*dx+dy*dy < rad*rad
			case 1: // a square
				in = math.Abs(dx) < rad*0.8 && math.Abs(dy) < rad*0.8
			default: // stripes
				in = (x+y+c*5)%14 < 5 && math.Abs(dx) < rad && math.Abs(dy) < rad
			}
			col := bg
			if in {
				col = fg
			}
			n := g.r.IntN(25) - 12
			img.Set(x, y, color.RGBA{jitter(col.R, n), jitter(col.G, n), jitter(col.B, n), 255})
		}
	}
	return img
}

func jitter(v uint8, n int) uint8 { return uint8(min(255, max(0, int(v)+n))) }

// hsv returns a colour from hue (0 to 1, wrapping), saturation and value.
func hsv(h, s, v float64) color.RGBA {
	h = math.Mod(h, 1) * 6
	i := math.Floor(h)
	f := h - i
	p, q, t := v*(1-s), v*(1-s*f), v*(1-s*(1-f))
	var r, g, b float64
	switch int(i) {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return color.RGBA{uint8(r * 255), uint8(g * 255), uint8(b * 255), 255}
}

// numberRange reads example numbers: their range, widened when they are
// all the same, whether they are whole, and whether none is negative.
func numberRange(examples []string) (lo, hi float64, whole, positive bool) {
	lo, hi, whole, positive = math.Inf(1), math.Inf(-1), true, true
	for _, e := range examples {
		f, ok := schema.ParseNumber(e)
		if !ok {
			continue
		}
		lo, hi = min(lo, f), max(hi, f)
		whole = whole && !strings.Contains(e, ".")
		positive = positive && f >= 0
	}
	if math.IsInf(lo, 1) {
		return 0, 1, false, true
	}
	if hi-lo < 1e-9 {
		pad := max(math.Abs(lo)*0.3, 1)
		lo, hi = lo-pad, hi+pad
	}
	return lo, hi, whole, positive
}

func formatNumber(v float64, whole, positive bool) string {
	if positive {
		v = max(v, 0)
	}
	if whole {
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// nearest returns the index of the example closest to v.
func nearest(examples []string, v float64) int {
	best, at := math.Inf(1), 0
	for i, e := range examples {
		if f, ok := schema.ParseNumber(e); ok && math.Abs(f-v) < best {
			best, at = math.Abs(f-v), i
		}
	}
	return at
}

// normal is the share of a standard normal below z.
func normal(z float64) float64 { return 0.5 * (1 + math.Erf(z/math.Sqrt2)) }

// yesNo reads yes or no words.
func yesNo(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "y", "true", "1":
		return true, true
	case "no", "n", "false", "0":
		return false, true
	}
	return false, false
}
