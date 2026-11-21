// Package report aggregates result CSV files and writes summary CSVs,
// pgfplots data files and LaTeX tables. The LaTeX report includes these
// files, so no number in the report is typed by hand.
package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
)

// Stores in report order and their display names.
var Stores = []string{"postgres", "mariadb", "mongo", "valkey"}

var storeNames = map[string]string{"postgres": "PostgreSQL", "mariadb": "MariaDB", "mongo": "MongoDB", "valkey": "Valkey"}

type record map[string]string

func (r record) f(k string) float64 {
	v, _ := strconv.ParseFloat(r[k], 64)
	return v
}

func (r record) i(k string) int64 {
	v, _ := strconv.ParseInt(r[k], 10, 64)
	return v
}

func (r record) b(k string) bool { return r[k] == "true" }

func readCSV(path string) ([]record, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []record
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		r := record{}
		for i, h := range head {
			if i < len(row) {
				r[h] = row[i]
			}
		}
		out = append(out, r)
	}
}

// Report holds the raw rows of one or more result directories.
type Report struct {
	scenarios, contention, loads, stats []record
}

// Load reads result directories. Later directories win when they contain
// the same (size, store, ...) group, which allows re-running one segment.
func Load(dirs ...string) (*Report, error) {
	r := &Report{}
	for _, d := range dirs {
		for name, dst := range map[string]*[]record{
			"scenarios.csv": &r.scenarios, "contention.csv": &r.contention, "loads.csv": &r.loads, "stats.csv": &r.stats,
		} {
			rows, err := readCSV(filepath.Join(d, name))
			if err != nil {
				return nil, err
			}
			*dst = append(*dst, rows...)
		}
	}
	if len(r.scenarios)+len(r.contention) == 0 {
		return nil, fmt.Errorf("no results in %v", dirs)
	}
	return r, nil
}

// Stat is a summary of a sample.
type Stat struct {
	N                                    int
	Mean, Median, Std, Min, Max, P95, CV float64
}

func summarize(xs []float64) Stat {
	if len(xs) == 0 {
		return Stat{}
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	st := Stat{N: len(s), Mean: sum / float64(len(s)), Min: s[0], Max: s[len(s)-1]}
	if len(s)%2 == 1 {
		st.Median = s[len(s)/2]
	} else {
		st.Median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	st.P95 = s[min(len(s)-1, int(math.Ceil(0.95*float64(len(s))))-1)]
	if len(s) > 1 {
		var ss float64
		for _, x := range s {
			ss += (x - st.Mean) * (x - st.Mean)
		}
		st.Std = math.Sqrt(ss / float64(len(s)-1))
	}
	if st.Mean > 0 {
		st.CV = st.Std / st.Mean
	}
	return st
}

// ScenarioKey identifies a scenario group.
type ScenarioKey struct {
	Tickets  int64
	Store    string
	Indexes  bool
	Scenario string
}

type ScenarioRow struct {
	ScenarioKey
	Server, E2E, Prepare Stat
	Rows                 float64
	Errors, Timeouts     int
}

// Scenarios aggregates the matrix phase (overhead runs are separate).
func (r *Report) Scenarios() map[ScenarioKey]*ScenarioRow {
	type acc struct {
		server, e2e, prep, rows []float64
		errs, tos               int
	}
	groups := map[ScenarioKey]*acc{}
	for _, x := range r.scenarios {
		if x.b("warmup") || x["phase"] == "overhead" {
			continue
		}
		k := ScenarioKey{x.i("tickets"), x["store"], x.b("indexes"), x["scenario"]}
		a := groups[k]
		if a == nil {
			a = &acc{}
			groups[k] = a
		}
		switch {
		case x["error"] != "":
			a.errs++
		case x.b("timed_out"):
			a.tos++
		default:
			a.server = append(a.server, x.f("server_ms"))
			a.e2e = append(a.e2e, x.f("e2e_ms"))
			a.prep = append(a.prep, x.f("prepare_ms"))
			a.rows = append(a.rows, x.f("rows"))
		}
	}
	out := map[ScenarioKey]*ScenarioRow{}
	for k, a := range groups {
		out[k] = &ScenarioRow{ScenarioKey: k, Server: summarize(a.server), E2E: summarize(a.e2e), Prepare: summarize(a.prep),
			Rows: summarize(a.rows).Mean, Errors: a.errs, Timeouts: a.tos}
	}
	return out
}

// ContentionKey identifies a contention group.
type ContentionKey struct {
	Tickets  int64
	Store    string
	Strategy string
	Clients  int64
}

type ContentionRow struct {
	ContentionKey
	Throughput, Duration, Conflicts, Attempts, P50, P95, P99 Stat
	Consistent                                               bool
	Errors                                                   int64
	Rounds                                                   int
}

func (r *Report) Contention() map[ContentionKey]*ContentionRow {
	type acc struct {
		tput, dur, conf, att, p50, p95, p99 []float64
		consistent                          bool
		errs                                int64
	}
	groups := map[ContentionKey]*acc{}
	for _, x := range r.contention {
		k := ContentionKey{x.i("tickets"), x["store"], x["strategy"], x.i("clients")}
		a := groups[k]
		if a == nil {
			a = &acc{consistent: true}
			groups[k] = a
		}
		a.tput = append(a.tput, x.f("throughput_per_s"))
		a.dur = append(a.dur, x.f("duration_ms"))
		a.conf = append(a.conf, x.f("conflicts"))
		a.att = append(a.att, x.f("attempts"))
		a.p50 = append(a.p50, x.f("p50_ms"))
		a.p95 = append(a.p95, x.f("p95_ms"))
		a.p99 = append(a.p99, x.f("p99_ms"))
		a.consistent = a.consistent && x.b("consistent")
		a.errs += x.i("errors")
	}
	out := map[ContentionKey]*ContentionRow{}
	for k, a := range groups {
		out[k] = &ContentionRow{ContentionKey: k, Throughput: summarize(a.tput), Duration: summarize(a.dur), Conflicts: summarize(a.conf),
			Attempts: summarize(a.att), P50: summarize(a.p50), P95: summarize(a.p95), P99: summarize(a.p99),
			Consistent: a.consistent, Errors: a.errs, Rounds: len(a.tput)}
	}
	return out
}

// DefaultStrategy is the first strategy of each engine.
var DefaultStrategy = map[string]string{"postgres": "cond", "mariadb": "cond", "mongo": "atomic", "valkey": "lua"}

func (r *Report) sizes() []int64 {
	set := map[int64]bool{}
	for _, x := range r.scenarios {
		set[x.i("tickets")] = true
	}
	for _, x := range r.contention {
		set[x.i("tickets")] = true
	}
	var out []int64
	for s := range set {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func (r *Report) stores() []string {
	set := map[string]bool{}
	for _, x := range append(slices.Clone(r.scenarios), r.contention...) {
		set[x["store"]] = true
	}
	var out []string
	for _, s := range Stores {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

func clientsOf(c map[ContentionKey]*ContentionRow) []int64 {
	set := map[int64]bool{}
	for k := range c {
		set[k.Clients] = true
	}
	var out []int64
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// spearman returns the rank correlation of x and y.
func spearman(x, y []float64) float64 {
	rank := func(v []float64) []float64 {
		idx := make([]int, len(v))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
		r := make([]float64, len(v))
		for i := 0; i < len(idx); {
			j := i
			for j+1 < len(idx) && v[idx[j+1]] == v[idx[i]] {
				j++
			}
			avg := float64(i+j)/2 + 1
			for k := i; k <= j; k++ {
				r[idx[k]] = avg
			}
			i = j + 1
		}
		return r
	}
	if len(x) < 2 {
		return 0
	}
	rx, ry := rank(x), rank(y)
	var mx, my float64
	for i := range rx {
		mx += rx[i]
		my += ry[i]
	}
	mx /= float64(len(rx))
	my /= float64(len(ry))
	var num, dx, dy float64
	for i := range rx {
		num += (rx[i] - mx) * (ry[i] - my)
		dx += (rx[i] - mx) * (rx[i] - mx)
		dy += (ry[i] - my) * (ry[i] - my)
	}
	if dx == 0 || dy == 0 {
		return 0
	}
	return num / math.Sqrt(dx*dy)
}

// Pair compares a non-relational store against a relational one.
type Pair struct{ NoSQL, SQL string }

var Pairs = []Pair{{"valkey", "postgres"}, {"valkey", "mariadb"}, {"mongo", "postgres"}, {"mongo", "mariadb"}}

// H1Row is the advantage of NoSQL over SQL throughput per client count.
type H1Row struct {
	Tickets   int64
	Pair      Pair
	Clients   []int64
	Advantage []float64
	Rho       float64
	Grows     bool
}

// H1 evaluates hypothesis 1 on the default strategies.
func (r *Report) H1() []H1Row {
	c := r.Contention()
	clients := clientsOf(c)
	var out []H1Row
	for _, size := range r.sizes() {
		for _, p := range Pairs {
			row := H1Row{Tickets: size, Pair: p}
			for _, cl := range clients {
				a := c[ContentionKey{size, p.NoSQL, DefaultStrategy[p.NoSQL], cl}]
				b := c[ContentionKey{size, p.SQL, DefaultStrategy[p.SQL], cl}]
				if a == nil || b == nil || b.Throughput.Mean == 0 {
					continue
				}
				row.Clients = append(row.Clients, cl)
				row.Advantage = append(row.Advantage, a.Throughput.Mean/b.Throughput.Mean)
			}
			if len(row.Clients) < 2 {
				continue
			}
			xs := make([]float64, len(row.Clients))
			for i, v := range row.Clients {
				xs[i] = float64(v)
			}
			row.Rho = spearman(xs, row.Advantage)
			row.Grows = row.Rho > 0 && row.Advantage[len(row.Advantage)-1] > row.Advantage[0]
			out = append(out, row)
		}
	}
	return out
}

// H2Row is the spread between the fastest and the slowest engine.
type H2Row struct {
	Scenario string
	Indexes  bool
	Spread   map[int64]float64
	Grows    bool
}

// H2 evaluates the backup hypothesis: differences grow with data size.
func (r *Report) H2() []H2Row {
	s := r.Scenarios()
	sizes := r.sizes()
	if len(sizes) < 2 {
		return nil
	}
	var out []H2Row
	for _, def := range scenario.All {
		for _, idx := range []bool{false, true} {
			row := H2Row{Scenario: string(def.ID), Indexes: idx, Spread: map[int64]float64{}}
			for _, size := range sizes {
				lo, hi := math.Inf(1), 0.0
				n := 0
				for _, st := range r.stores() {
					v := s[ScenarioKey{size, st, idx, string(def.ID)}]
					if v == nil || v.Server.N == 0 {
						continue
					}
					lo, hi = math.Min(lo, v.Server.Mean), math.Max(hi, v.Server.Mean)
					n++
				}
				if n >= 2 && lo > 0 {
					row.Spread[size] = hi / lo
				}
			}
			first, ok1 := row.Spread[sizes[0]]
			last, ok2 := row.Spread[sizes[len(sizes)-1]]
			if !ok1 || !ok2 {
				continue
			}
			row.Grows = last > first
			out = append(out, row)
		}
	}
	return out
}

// Findings returns one-line conclusions for the terminal.
func (r *Report) Findings() []string {
	var out []string
	h1 := r.H1()
	grow := 0
	for _, x := range h1 {
		if x.Grows {
			grow++
		}
	}
	if len(h1) > 0 {
		out = append(out, fmt.Sprintf("H1: NoSQL advantage grows with clients in %d of %d (size, pair) cases -> %s", grow, len(h1), verdict(grow, len(h1))))
	}
	h2 := r.H2()
	grow = 0
	for _, x := range h2 {
		if x.Grows {
			grow++
		}
	}
	if len(h2) > 0 {
		out = append(out, fmt.Sprintf("H2: engine spread grows with size in %d of %d (scenario, index mode) cases -> %s", grow, len(h2), verdict(grow, len(h2))))
	}
	bad := 0
	for _, c := range r.Contention() {
		if !c.Consistent {
			bad++
		}
	}
	out = append(out, fmt.Sprintf("contention: %d inconsistent groups", bad))
	return out
}

func verdict(yes, all int) string {
	switch {
	case all == 0:
		return "brak danych"
	case float64(yes) >= 0.75*float64(all):
		return "potwierdzona"
	case float64(yes) >= 0.4*float64(all):
		return "potwierdzona częściowo"
	default:
		return "odrzucona"
	}
}

func human(n int64) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%d mln", n/1_000_000)
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%d tys.", n/1000)
	}
	return strconv.FormatInt(n, 10)
}

func texEscape(s string) string {
	r := strings.NewReplacer("&", `\&`, "%", `\%`, "_", `\_`, "#", `\#`, "$", `\$`)
	return r.Replace(s)
}
