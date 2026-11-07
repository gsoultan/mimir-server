// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package arch

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Nearby recommendations: what customers of the same kind within a
// distance buy, that this customer doesn't yet.
var (
	latWords      = regexp.MustCompile(`^lat$|latitude|^lat_|_lat$`)
	lonWords      = regexp.MustCompile(`^lon$|^lng$|longitude|^lon_|^lng_|_lon$|_lng$`)
	categoryWords = regexp.MustCompile(`categor|segment|type|class|industry|tier|channel|group|kind`)
)

const (
	// earthKm is the Earth's mean radius.
	earthKm = 6371.0
	// noPlace marks a latitude or longitude that isn't known. It is far
	// outside any real one.
	noPlace = 999
	// RadiusColumn names the question's distance, in kilometres.
	RadiusColumn = "radius_km"
	// maxNearbyCells bounds customers × products, the size of the table of
	// who buys what that a nearby model keeps.
	maxNearbyCells = 10_000_000
	// nearbyNeighbours is how many customers of the same kind the
	// default distance reaches for a typical customer.
	nearbyNeighbours = 10
)

// NearbyShape sizes a Nearby.
type NearbyShape struct {
	Customers  int `json:"customers"`
	Products   int `json:"products"`
	Categories int `json:"categories"`
}

// Nearby scores products for a customer from what customers of the same
// category within a distance buy. Forward maps x, [batch, 5] (the
// customer's index, the category's index, latitude and longitude in
// degrees, and the distance in kilometres), to [batch, Products] scores:
// more for a product more nearby customers buy, far less for one the
// customer already buys. A latitude or longitude of noPlace takes the
// customer's own; an unknown category takes the customer's own, and with
// neither, customers of every category count. Without a place, every
// customer counts, near or far.
type Nearby struct {
	// Lat and Lon are each customer's place in radians, or noPlace;
	// the last row is for a customer it hasn't seen.
	Lat, Lon *tensor.Param // Float32 [Customers+1, 1]
	// Cat is each customer's category index; Categories when unknown.
	Cat *tensor.Param // Float32 [Customers+1, 1]
	// Buys is 1 where a customer bought a product.
	Buys *tensor.Param // Float32 [Customers+1, Products]
	// Share is the share of a category's customers who buy each product;
	// the last row is over every customer.
	Share *tensor.Param // Float32 [Categories+1, Products]
	shape NearbyShape
}

// NewNearby returns a Nearby of shape s whose tables are zero, for saved
// weights to be loaded into.
func NewNearby(s NearbyShape) *Nearby {
	c, p := s.Customers+1, max(s.Products, 1)
	z := func(r, c int) *tensor.Param { return tensor.NewBuffer(tensor.Zeros(tensor.Float32, r, c)) }
	return &Nearby{Lat: z(c, 1), Lon: z(c, 1), Cat: z(c, 1), Buys: z(c, p), Share: z(s.Categories+1, p), shape: s}
}

// Forward scores every product for each question in x.
func (m *Nearby) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := m.shape
	if x.Rank() != 2 || x.Shape()[1] != 5 {
		return tensor.Failed(fmt.Errorf("nearby: input %v, want [batch, 5]: %w", x.Shape(), tensor.ErrShape))
	}
	n, c, p := x.Shape()[0], s.Customers+1, s.Products
	f := func(v float64) *tensor.Tensor { return tensor.Scalar[float32](float32(v)) }
	one := f(1)
	col := func(j int) *tensor.Tensor { return x.Shrink([][2]int{{0, n}, {j, j + 1}}) }
	// pick is a where b is 1 and c where it is 0.
	pick := func(b, a, c *tensor.Tensor) *tensor.Tensor { return b.Mul(a).Add(one.Sub(b).Mul(c)) }
	// clean reads NaN, an empty cell, as fill.
	clean := func(v *tensor.Tensor, fill float64) *tensor.Tensor { return tensor.Where(v.Equal(v), v, f(fill)) }

	who := clean(col(0), float64(s.Customers)).Maximum(f(0)).Minimum(f(float64(s.Customers))).Cast(tensor.Int32)
	row := func(t *tensor.Param) *tensor.Tensor { return t.Value().Gather(0, who) } // [n, 1]

	// The place asked about, else the customer's own.
	lat, lon := clean(col(2), noPlace), clean(col(3), noPlace)
	given := lat.Abs().Less(f(90.000001)).Mul(lon.Abs().Less(f(180.000001)))
	rad := f(math.Pi / 180)
	lat = pick(given, lat.Mul(rad), row(m.Lat))
	lon = pick(given, lon.Mul(rad), row(m.Lon))
	placed := lat.Abs().Less(f(2)) // [n, 1]

	allLat, allLon := m.Lat.Value().Reshape(1, c), m.Lon.Value().Reshape(1, c)
	there := allLat.Abs().Less(f(2)) // [1, c]
	// Haversine: a grows with distance, and is within sin²(r/2R) for
	// customers within r.
	half := func(t *tensor.Tensor) *tensor.Tensor { s := t.Mul(f(0.5)).Sin(); return s.Mul(s) }
	a := half(allLat.Sub(lat)).Add(lat.Cos().Mul(allLat.Cos()).Mul(half(allLon.Sub(lon)))) // [n, c]
	r := clean(col(4), 0).Maximum(f(0.01)).Div(f(2 * earthKm)).Minimum(f(math.Pi / 2))
	limit := r.Sin().Mul(r.Sin())
	near := a.Less(limit.Add(f(1e-12))).Mul(there)
	near = pick(placed, near, one.Add(a.Mul(f(0)))) // without a place, everyone

	// The category asked about, else the customer's own.
	cat := clean(col(1), float64(s.Categories))
	cat = pick(cat.Less(f(float64(s.Categories))), cat, row(m.Cat))
	catKnown := cat.Less(f(float64(s.Categories)))
	same := pick(catKnown, m.Cat.Value().Reshape(1, c).Equal(cat), one)

	self := tensor.Arange[float32](0, float32(c), 1).Reshape(1, c).Equal(who.Cast(tensor.Float32))
	w := near.Mul(same).Mul(one.Sub(self))
	counts := w.MatMul(m.Buys.Value()) // [n, p]

	bought := m.Buys.Value().Gather(0, who.Expand(n, p))
	shareRow := cat.Maximum(f(0)).Minimum(f(float64(s.Categories))).Cast(tensor.Int32).Expand(n, p)
	share := m.Share.Value().Gather(0, shareRow)
	// Nearby customers count first; how common a product is in the
	// category breaks ties and answers when nobody is near.
	return counts.Add(one).Log().Add(share.Mul(f(1e-3))).Sub(bought.Mul(f(1e3)))
}

// nearbyModel is what a nearby learner works out, before it is a module.
type nearbyModel struct {
	lat, lon []float64 // radians, or noPlace
	cat      []int     // category index, or categories when unknown
	buys     []map[int]bool
	share    [][]float32 // [categories+1][products]
	products int
	cats     int
}

func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	h := func(v float64) float64 { s := math.Sin(v / 2); return s * s }
	a := h(lat2-lat1) + math.Cos(lat1)*math.Cos(lat2)*h(lon2-lon1)
	return 2 * earthKm * math.Asin(math.Sqrt(min(1, a)))
}

// top returns the n products the model would offer customer u within
// radius km, leaving out skip and what u bought.
func (m *nearbyModel) top(u int, radius float64, n int) []int {
	count := make([]float64, m.products)
	placed := m.lat[u] != noPlace
	for v := range m.lat {
		if v == u || m.cat[u] < m.cats && m.cat[v] != m.cat[u] {
			continue
		}
		if placed && (m.lat[v] == noPlace || distanceKm(m.lat[u], m.lon[u], m.lat[v], m.lon[v]) > radius) {
			continue
		}
		for p := range m.buys[v] {
			count[p]++
		}
	}
	share := m.share[min(m.cat[u], m.cats)]
	idx := make([]int, 0, m.products)
	score := make([]float64, m.products)
	for p := range m.products {
		if m.buys[u][p] {
			continue
		}
		score[p] = math.Log(count[p]+1) + 1e-3*float64(share[p])
		idx = append(idx, p)
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(score[b], score[a]) })
	return idx[:min(n, len(idx))]
}

// defaultRadius is a distance that reaches about nearbyNeighbours
// customers of the same category for a typical customer, rounded to a
// round number of kilometres.
func (m *nearbyModel) defaultRadius(r *rand.Rand) float64 {
	var placed []int
	for u := range m.lat {
		if m.lat[u] != noPlace {
			placed = append(placed, u)
		}
	}
	if len(placed) < 2 {
		return 25
	}
	sample := placed
	if len(sample) > 300 {
		sample = make([]int, 300)
		for i, k := range r.Perm(len(placed))[:300] {
			sample[i] = placed[k]
		}
	}
	var reach []float64
	for _, u := range sample {
		var d []float64
		for _, v := range placed {
			if v != u && (m.cat[u] == m.cats || m.cat[v] == m.cat[u]) {
				d = append(d, distanceKm(m.lat[u], m.lon[u], m.lat[v], m.lon[v]))
			}
		}
		if len(d) == 0 {
			continue
		}
		slices.Sort(d)
		reach = append(reach, d[min(nearbyNeighbours, len(d))-1])
	}
	if len(reach) == 0 {
		return 25
	}
	slices.Sort(reach)
	km := reach[len(reach)/2]
	for _, round := range []float64{1, 2, 5, 10, 15, 20, 25, 30, 40, 50, 75, 100, 150, 200, 300, 500, 1000} {
		if km <= round {
			return round
		}
	}
	return 2000
}

// nearbyRecommender learns what customers of each category buy where.
// The answer column is the product; the customer, latitude and longitude
// columns are found by their names, and so is a category column if there
// is one.
var nearbyRecommender = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		item := slices.Index(c.Header, c.Target)
		if item < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		var others []int
		for j := range c.Header {
			if j != item {
				others = append(others, j)
			}
		}
		lat, lon := pick(c.Header, others, latWords), pick(c.Header, others, lonWords)
		if lat < 0 || lon < 0 || lat == lon || !numbers(c.column(lat)) || !numbers(c.column(lon)) {
			return Learned{}, cellsError("no columns give each customer's latitude and longitude; name them latitude and longitude, in degrees")
		}
		var rest []int
		for _, j := range others {
			if j != lat && j != lon {
				rest = append(rest, j)
			}
		}
		who := pick(c.Header, rest, whoWords)
		if who < 0 {
			return Learned{}, cellsError("no column says who bought each product; name one customer, client or account")
		}
		category := -1
		for _, j := range rest {
			if j != who && categoryWords.MatchString(strings.ToLower(c.Header[j])) && !numbers(c.column(j)) {
				category = j
				break
			}
		}

		customers, products := index(c.column(who)), index(c.column(item))
		if len(products) < 2 {
			return Learned{}, cellsError("the %q column names only one product, so there is nothing to recommend", c.Target)
		}
		if len(products) > 1000 {
			return Learned{}, cellsError("the %q column names %d products; at most 1000 can be recommended", c.Target, len(products))
		}
		if (len(customers)+1)*len(products) > maxNearbyCells {
			return Learned{}, cellsError("%d customers and %d products are too many to keep; at most %d customers × products", len(customers), len(products), maxNearbyCells)
		}
		var cats []string
		if category >= 0 {
			cats = index(c.column(category))
		}
		uid, pid, cid := map[string]int{}, map[string]int{}, map[string]int{}
		for i, v := range customers {
			uid[v] = i
		}
		for i, v := range products {
			pid[v] = i
		}
		for i, v := range cats {
			cid[v] = i
		}

		m := &nearbyModel{products: len(products), cats: len(cats)}
		n := len(customers)
		m.lat, m.lon, m.cat, m.buys = make([]float64, n), make([]float64, n), make([]int, n), make([]map[int]bool, n)
		sumLat, sumLon, places := make([]float64, n), make([]float64, n), make([]int, n)
		catCount := make([]map[int]int, n)
		for u := range n {
			m.buys[u], catCount[u] = map[int]bool{}, map[int]int{}
		}
		for _, r := range c.Rows {
			u, ok := uid[strings.TrimSpace(r[who])]
			if !ok {
				continue
			}
			if p, ok := pid[strings.TrimSpace(r[item])]; ok {
				m.buys[u][p] = true
			}
			if category >= 0 {
				if k, ok := cid[strings.TrimSpace(r[category])]; ok {
					catCount[u][k]++
				}
			}
			y, okY := schema.ParseNumber(r[lat])
			x, okX := schema.ParseNumber(r[lon])
			if okY && okX && math.Abs(y) <= 90 && math.Abs(x) <= 180 && (y != 0 || x != 0) {
				sumLat[u] += y
				sumLon[u] += x
				places[u]++
			}
		}
		for u := range n {
			m.lat[u], m.lon[u] = noPlace, noPlace
			if places[u] > 0 {
				m.lat[u] = sumLat[u] / float64(places[u]) * math.Pi / 180
				m.lon[u] = sumLon[u] / float64(places[u]) * math.Pi / 180
			}
			m.cat[u] = len(cats)
			best := 0
			for k, v := range catCount[u] {
				if v > best || v == best && k < m.cat[u] {
					m.cat[u], best = k, v
				}
			}
		}
		m.share = make([][]float32, len(cats)+1)
		size := make([]int, len(cats)+1)
		for k := range m.share {
			m.share[k] = make([]float32, len(products))
		}
		for u := range n {
			for _, k := range []int{m.cat[u], len(cats)} {
				size[k]++
				for p := range m.buys[u] {
					m.share[k][p]++
				}
				if m.cat[u] == len(cats) {
					break
				}
			}
		}
		for k := range m.share {
			for p := range m.share[k] {
				m.share[k][p] /= float32(max(size[k], 1))
			}
		}
		if progress != nil {
			progress(1, 3)
		}
		if err := ctx.Err(); err != nil {
			return Learned{}, err
		}

		rng := rand.New(rand.NewPCG(7, 11))
		radius := m.defaultRadius(rng)
		// Score it by hiding one product of each customer who bought
		// several, and checking it is among the 10 offered.
		hits, tried := 0, 0
		for _, u := range rng.Perm(n) {
			if tried == 2000 {
				break
			}
			if len(m.buys[u]) < 2 {
				continue
			}
			bought := make([]int, 0, len(m.buys[u]))
			for p := range m.buys[u] {
				bought = append(bought, p)
			}
			slices.Sort(bought)
			hide := bought[rng.IntN(len(bought))]
			delete(m.buys[u], hide)
			if slices.Contains(m.top(u, radius, recommendTop), hide) {
				hits++
			}
			m.buys[u][hide] = true
			tried++
		}
		if progress != nil {
			progress(2, 3)
		}

		shape := NearbyShape{Customers: n, Products: len(products), Categories: len(cats)}
		fill := func(rows, cols int, at func(r, c int) float32) *tensor.Param {
			v := make([]float32, rows*cols)
			for r := range rows {
				for c := range cols {
					v[r*cols+c] = at(r, c)
				}
			}
			return tensor.NewBuffer(tensor.FromSlice(v, rows, cols))
		}
		mod := &Nearby{shape: shape}
		mod.Lat = fill(n+1, 1, func(r, _ int) float32 {
			if r == n {
				return noPlace
			}
			return float32(m.lat[r])
		})
		mod.Lon = fill(n+1, 1, func(r, _ int) float32 {
			if r == n {
				return noPlace
			}
			return float32(m.lon[r])
		})
		mod.Cat = fill(n+1, 1, func(r, _ int) float32 {
			if r == n {
				return float32(len(cats))
			}
			return float32(m.cat[r])
		})
		mod.Buys = fill(n+1, len(products), func(r, c int) float32 {
			if r < n && m.buys[r][c] {
				return 1
			}
			return 0
		})
		mod.Share = fill(len(cats)+1, len(products), func(r, c int) float32 { return m.share[r][c] })

		catName := "customer_category"
		if category >= 0 {
			catName = c.Header[category]
		}
		cols := []schema.Column{
			{Name: c.Header[who], Kind: schema.Key, Values: customers},
			{Name: catName, Kind: schema.Key, Values: cats},
			{Name: c.Header[lat], Kind: schema.Number, Fill: noPlace},
			{Name: c.Header[lon], Kind: schema.Number, Fill: noPlace},
			{Name: RadiusColumn, Kind: schema.Number, Fill: radius},
		}
		s, err := shapeJSON(shape)
		return Learned{Module: mod, Shape: s, Columns: cols, Labels: products, Accuracy: float64(hits) / float64(max(tried, 1))}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s NearbyShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a nearby recommender's shape: %w", err)
		}
		return NewNearby(s), nil
	},
}
