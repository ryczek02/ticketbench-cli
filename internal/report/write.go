package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
)

type out struct {
	dir   string
	files []string
}

func (o *out) path(rel string) (string, error) {
	p := filepath.Join(o.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	o.files = append(o.files, rel)
	return p, nil
}

func (o *out) text(rel, body string) error {
	p, err := o.path(rel)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

func (o *out) csv(rel string, rows [][]string) error {
	p, err := o.path(rel)
	if err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// dat writes a whitespace separated pgfplots table; NaN becomes "nan".
func (o *out) dat(rel string, head []string, rows [][]float64) error {
	var b strings.Builder
	b.WriteString(strings.Join(head, " "))
	b.WriteByte('\n')
	for _, r := range rows {
		for i, v := range r {
			if i > 0 {
				b.WriteByte(' ')
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				b.WriteString("nan")
			} else {
				b.WriteString(strconv.FormatFloat(v, 'g', 6, 64))
			}
		}
		b.WriteByte('\n')
	}
	return o.text(rel, b.String())
}

func g(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

// num formats a value for siunitx \num{}.
func num(v float64) string {
	switch {
	case math.IsNaN(v):
		return "--"
	case v == 0:
		return `\num{0}`
	case math.Abs(v) < 0.1:
		return fmt.Sprintf(`\num{%.3f}`, v)
	case math.Abs(v) < 10:
		return fmt.Sprintf(`\num{%.2f}`, v)
	case math.Abs(v) < 1000:
		return fmt.Sprintf(`\num{%.1f}`, v)
	}
	return fmt.Sprintf(`\num{%.0f}`, v)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Write produces every output file and returns their relative paths.
func (r *Report) Write(dir string) ([]string, error) {
	o := &out{dir: dir}
	steps := []func(*out) error{r.writeScenarios, r.writeContention, r.writeLoads, r.writeStats, r.writeOverhead, r.writeHypotheses, r.writeValues, r.writePlans, r.writeSecurity}
	for _, s := range steps {
		if err := s(o); err != nil {
			return o.files, err
		}
	}
	return o.files, nil
}

func (r *Report) writeScenarios(o *out) error {
	sc := r.Scenarios()
	keys := make([]ScenarioKey, 0, len(sc))
	for k := range sc {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b ScenarioKey) int {
		if a.Tickets != b.Tickets {
			return int(a.Tickets - b.Tickets)
		}
		if a.Scenario != b.Scenario {
			return strings.Compare(a.Scenario, b.Scenario)
		}
		if a.Indexes != b.Indexes {
			if a.Indexes {
				return 1
			}
			return -1
		}
		return slices.Index(Stores, a.Store) - slices.Index(Stores, b.Store)
	})
	rows := [][]string{{"tickets", "store", "indexes", "scenario", "op", "n", "mean_ms", "median_ms", "std_ms", "cv", "min_ms", "max_ms", "p95_ms",
		"e2e_mean_ms", "grpc_overhead_ms", "prepare_mean_ms", "rows_mean", "errors", "timeouts"}}
	for _, k := range keys {
		v := sc[k]
		def, _ := scenario.Lookup(scenario.ID(k.Scenario))
		rows = append(rows, []string{strconv.FormatInt(k.Tickets, 10), k.Store, onOff(k.Indexes), k.Scenario, string(def.Op), strconv.Itoa(v.Server.N),
			g(v.Server.Mean), g(v.Server.Median), g(v.Server.Std), g(v.Server.CV), g(v.Server.Min), g(v.Server.Max), g(v.Server.P95),
			g(v.E2E.Mean), g(v.E2E.Mean - v.Server.Mean), g(v.Prepare.Mean), g(v.Rows), strconv.Itoa(v.Errors), strconv.Itoa(v.Timeouts)})
	}
	if err := o.csv("data/scenarios_summary.csv", rows); err != nil {
		return err
	}

	sizes, stores := r.sizes(), r.stores()
	mean := func(size int64, st string, idx bool, id string) float64 {
		v := sc[ScenarioKey{size, st, idx, id}]
		if v == nil || v.Server.N == 0 {
			return math.NaN()
		}
		return v.Server.Mean
	}
	// One pgfplots file per scenario and index mode: x = size.
	for _, def := range scenario.All {
		for _, idx := range []bool{false, true} {
			var data [][]float64
			for _, size := range sizes {
				row := []float64{float64(size)}
				for _, st := range Stores {
					row = append(row, mean(size, st, idx, string(def.ID)))
				}
				data = append(data, row)
			}
			if err := o.dat(fmt.Sprintf("plots/scen_%s_%s.dat", def.ID, onOff(idx)), append([]string{"tickets"}, Stores...), data); err != nil {
				return err
			}
		}
	}
	// Index speedup per size: off / on.
	for _, size := range sizes {
		var data [][]float64
		for i, def := range scenario.All {
			row := []float64{float64(i)}
			for _, st := range Stores {
				row = append(row, mean(size, st, false, string(def.ID))/mean(size, st, true, string(def.ID)))
			}
			data = append(data, row)
		}
		if err := o.dat(fmt.Sprintf("plots/speedup_%d.dat", size), append([]string{"i"}, Stores...), data); err != nil {
			return err
		}
	}
	// LaTeX table per size.
	for _, size := range sizes {
		var b strings.Builder
		cols := strings.Repeat("rr", len(stores))
		fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{ll%s}\n\\toprule\n", cols)
		b.WriteString(" & ")
		for _, st := range stores {
			fmt.Fprintf(&b, "& \\multicolumn{2}{c}{%s} ", storeNames[st])
		}
		b.WriteString("\\\\\n")
		for i := range stores {
			fmt.Fprintf(&b, "\\cmidrule(lr){%d-%d}", 3+2*i, 4+2*i)
		}
		b.WriteString("\nID & Scenariusz ")
		for range stores {
			b.WriteString("& bez ind. & z ind. ")
		}
		b.WriteString("\\\\\n\\midrule\n")
		for _, def := range scenario.All {
			best := map[bool]float64{false: math.Inf(1), true: math.Inf(1)}
			for _, st := range stores {
				for _, idx := range []bool{false, true} {
					if v := mean(size, st, idx, string(def.ID)); !math.IsNaN(v) {
						best[idx] = math.Min(best[idx], v)
					}
				}
			}
			fmt.Fprintf(&b, "%s & %s ", def.ID, texEscape(def.Name))
			for _, st := range stores {
				for _, idx := range []bool{false, true} {
					v := sc[ScenarioKey{size, st, idx, string(def.ID)}]
					cell := "--"
					switch {
					case v == nil:
					case v.Server.N == 0 && v.Timeouts > 0:
						cell = "t/o"
					case v.Server.N == 0:
						cell = "błąd"
					default:
						cell = num(v.Server.Mean)
						if v.Server.Mean == best[idx] {
							cell = "\\textbf{" + cell + "}"
						}
					}
					b.WriteString("& " + cell + " ")
				}
			}
			b.WriteString("\\\\\n")
			if def.ID == "C6" || def.ID == "R6" || def.ID == "U6" || def.ID == "D6" {
				b.WriteString("\\midrule\n")
			}
		}
		b.WriteString("\\bottomrule\n\\end{tabular}\n")
		if err := o.text(fmt.Sprintf("tables/scenarios_%d.tex", size), b.String()); err != nil {
			return err
		}
	}
	// gRPC hop: mean (e2e - server) per store, over all groups.
	var b strings.Builder
	b.WriteString("% generated by tbench report\n\\begin{tabular}{lrrr}\n\\toprule\nSZBD & mediana narzutu [ms] & średni narzut [ms] & udział w czasie R1 [\\%] \\\\\n\\midrule\n")
	for _, st := range stores {
		var over []float64
		var r1 []float64
		for k, v := range sc {
			if k.Store != st || v.Server.N == 0 {
				continue
			}
			over = append(over, v.E2E.Mean-v.Server.Mean)
			if k.Scenario == "R1" && v.E2E.Mean > 0 {
				r1 = append(r1, 100*(v.E2E.Mean-v.Server.Mean)/v.E2E.Mean)
			}
		}
		s := summarize(over)
		fmt.Fprintf(&b, "%s & %s & %s & %s \\\\\n", storeNames[st], num(s.Median), num(s.Mean), num(summarize(r1).Mean))
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	return o.text("tables/grpc.tex", b.String())
}

func (r *Report) writeContention(o *out) error {
	c := r.Contention()
	if len(c) == 0 {
		return nil
	}
	keys := make([]ContentionKey, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b ContentionKey) int {
		switch {
		case a.Tickets != b.Tickets:
			return int(a.Tickets - b.Tickets)
		case a.Store != b.Store:
			return slices.Index(Stores, a.Store) - slices.Index(Stores, b.Store)
		case a.Strategy != b.Strategy:
			return strings.Compare(a.Strategy, b.Strategy)
		}
		return int(a.Clients - b.Clients)
	})
	rows := [][]string{{"tickets", "store", "strategy", "clients", "rounds", "throughput_mean", "throughput_std", "duration_mean_ms",
		"attempts_mean", "conflicts_mean", "p50_mean_ms", "p95_mean_ms", "p99_mean_ms", "errors", "consistent"}}
	for _, k := range keys {
		v := c[k]
		rows = append(rows, []string{strconv.FormatInt(k.Tickets, 10), k.Store, k.Strategy, strconv.FormatInt(k.Clients, 10), strconv.Itoa(v.Rounds),
			g(v.Throughput.Mean), g(v.Throughput.Std), g(v.Duration.Mean), g(v.Attempts.Mean), g(v.Conflicts.Mean),
			g(v.P50.Mean), g(v.P95.Mean), g(v.P99.Mean), strconv.FormatInt(v.Errors, 10), strconv.FormatBool(v.Consistent)})
	}
	if err := o.csv("data/contention_summary.csv", rows); err != nil {
		return err
	}
	clients := clientsOf(c)
	strategies := map[string][]string{}
	for k := range c {
		if !slices.Contains(strategies[k.Store], k.Strategy) {
			strategies[k.Store] = append(strategies[k.Store], k.Strategy)
		}
	}
	for st := range strategies {
		slices.Sort(strategies[st])
	}
	for _, size := range r.sizes() {
		get := func(st, strat string, cl int64) *ContentionRow { return c[ContentionKey{size, st, strat, cl}] }
		var tput, p95 [][]float64
		for _, cl := range clients {
			rt, rp := []float64{float64(cl)}, []float64{float64(cl)}
			for _, st := range Stores {
				if v := get(st, DefaultStrategy[st], cl); v != nil {
					rt, rp = append(rt, v.Throughput.Mean), append(rp, v.P95.Mean)
				} else {
					rt, rp = append(rt, math.NaN()), append(rp, math.NaN())
				}
			}
			tput, p95 = append(tput, rt), append(p95, rp)
		}
		if err := o.dat(fmt.Sprintf("plots/contention_%d.dat", size), append([]string{"clients"}, Stores...), tput); err != nil {
			return err
		}
		if err := o.dat(fmt.Sprintf("plots/contention_p95_%d.dat", size), append([]string{"clients"}, Stores...), p95); err != nil {
			return err
		}
		for _, st := range Stores {
			if len(strategies[st]) == 0 {
				continue
			}
			var data [][]float64
			for _, cl := range clients {
				row := []float64{float64(cl)}
				for _, strat := range strategies[st] {
					if v := get(st, strat, cl); v != nil {
						row = append(row, v.Throughput.Mean)
					} else {
						row = append(row, math.NaN())
					}
				}
				data = append(data, row)
			}
			if err := o.dat(fmt.Sprintf("plots/strategies_%s_%d.dat", st, size), append([]string{"clients"}, strategies[st]...), data); err != nil {
				return err
			}
		}
		// Table: every strategy, every client count.
		var b strings.Builder
		fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{ll%s}\n\\toprule\n", strings.Repeat("r", len(clients)))
		b.WriteString("SZBD & strategia ")
		for _, cl := range clients {
			fmt.Fprintf(&b, "& %d kl. ", cl)
		}
		b.WriteString("\\\\\n\\midrule\n")
		for _, st := range Stores {
			for _, strat := range strategies[st] {
				name := strat
				if strat == DefaultStrategy[st] {
					name += "$^{*}$"
				}
				fmt.Fprintf(&b, "%s & \\texttt{%s} ", storeNames[st], name)
				for _, cl := range clients {
					v := get(st, strat, cl)
					cell := "--"
					if v != nil {
						cell = num(v.Throughput.Mean)
						if !v.Consistent {
							cell += "$^{!}$"
						}
					}
					b.WriteString("& " + cell + " ")
				}
				b.WriteString("\\\\\n")
			}
		}
		b.WriteString("\\bottomrule\n\\end{tabular}\n")
		if err := o.text(fmt.Sprintf("tables/strategies_%d.tex", size), b.String()); err != nil {
			return err
		}
		// Conflicts and p95 for the default strategies.
		b.Reset()
		fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{lr%s}\n\\toprule\n", strings.Repeat("rr", len(Stores)))
		b.WriteString("klienci & ")
		for i, st := range Stores {
			if i > 0 {
				b.WriteString("& ")
			}
			fmt.Fprintf(&b, "\\multicolumn{2}{c}{%s} ", storeNames[st])
		}
		b.WriteString("\\\\\n & ")
		for i := range Stores {
			if i > 0 {
				b.WriteString("& ")
			}
			b.WriteString("konfl. & p95 [ms] ")
		}
		b.WriteString("\\\\\n\\midrule\n")
		for _, cl := range clients {
			fmt.Fprintf(&b, "%d ", cl)
			for _, st := range Stores {
				if v := get(st, DefaultStrategy[st], cl); v != nil {
					fmt.Fprintf(&b, "& %s & %s ", num(v.Conflicts.Mean), num(v.P95.Mean))
				} else {
					b.WriteString("& -- & -- ")
				}
			}
			b.WriteString("\\\\\n")
		}
		b.WriteString("\\bottomrule\n\\end{tabular}\n")
		if err := o.text(fmt.Sprintf("tables/contention_%d.tex", size), b.String()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Report) writeLoads(o *out) error {
	type key struct {
		tickets int64
		store   string
		kind    string
	}
	vals := map[key][]float64{}
	for _, x := range r.loads {
		if isSecurity(x) {
			continue
		}
		k := key{x.i("tickets"), x["store"], x["kind"]}
		vals[k] = append(vals[k], x.f("server_ms")/1000)
	}
	rows := [][]string{{"tickets", "store", "kind", "seconds_mean", "n"}}
	sizes := r.sizes()
	for _, size := range sizes {
		for _, st := range Stores {
			for _, kind := range []string{"load", "indexes_on", "indexes_off", "reset"} {
				if v := vals[key{size, st, kind}]; len(v) > 0 {
					s := summarize(v)
					rows = append(rows, []string{strconv.FormatInt(size, 10), st, kind, g(s.Mean), strconv.Itoa(s.N)})
				}
			}
		}
	}
	if err := o.csv("data/loads_summary.csv", rows); err != nil {
		return err
	}
	for _, kind := range []string{"load", "indexes_on"} {
		var data [][]float64
		for _, size := range sizes {
			row := []float64{float64(size)}
			for _, st := range Stores {
				if v := vals[key{size, st, kind}]; len(v) > 0 {
					row = append(row, summarize(v).Mean)
				} else {
					row = append(row, math.NaN())
				}
			}
			data = append(data, row)
		}
		if err := o.dat("plots/"+kind+".dat", append([]string{"tickets"}, Stores...), data); err != nil {
			return err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{l%s}\n\\toprule\n", strings.Repeat("rr", len(sizes)))
	b.WriteString("SZBD ")
	for _, s := range sizes {
		fmt.Fprintf(&b, "& \\multicolumn{2}{c}{%s} ", human(s))
	}
	b.WriteString("\\\\\n")
	for range sizes {
		b.WriteString("& import [s] & indeksy [s] ")
	}
	b.WriteString("\\\\\n\\midrule\n")
	for _, st := range Stores {
		b.WriteString(storeNames[st] + " ")
		for _, s := range sizes {
			for _, kind := range []string{"load", "indexes_on"} {
				if v := vals[key{s, st, kind}]; len(v) > 0 {
					b.WriteString("& " + num(summarize(v).Mean) + " ")
				} else {
					b.WriteString("& -- ")
				}
			}
		}
		b.WriteString("\\\\\n")
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	return o.text("tables/load.tex", b.String())
}

func (r *Report) writeStats(o *out) error {
	type key struct {
		tickets int64
		store   string
		idx     bool
	}
	last := map[key]record{}
	for _, x := range r.stats {
		if isSecurity(x) {
			continue
		}
		last[key{x.i("tickets"), x["store"], x.b("indexes")}] = x
	}
	rows := [][]string{{"tickets", "store", "indexes", "total_mb", "data_mb", "index_mb", "rows"}}
	sizes := r.sizes()
	mb := func(v int64) float64 { return float64(v) / (1 << 20) }
	for _, size := range sizes {
		for _, st := range Stores {
			for _, idx := range []bool{false, true} {
				if x, ok := last[key{size, st, idx}]; ok {
					rows = append(rows, []string{strconv.FormatInt(size, 10), st, onOff(idx), g(mb(x.i("total_bytes"))), g(mb(x.i("data_bytes"))), g(mb(x.i("index_bytes"))), x["rows"]})
				}
			}
		}
	}
	if err := o.csv("data/stats_summary.csv", rows); err != nil {
		return err
	}
	for _, idx := range []bool{false, true} {
		var data [][]float64
		for _, size := range sizes {
			row := []float64{float64(size)}
			for _, st := range Stores {
				if x, ok := last[key{size, st, idx}]; ok {
					row = append(row, mb(x.i("total_bytes")))
				} else {
					row = append(row, math.NaN())
				}
			}
			data = append(data, row)
		}
		if err := o.dat("plots/storage_"+onOff(idx)+".dat", append([]string{"tickets"}, Stores...), data); err != nil {
			return err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{l%s}\n\\toprule\n", strings.Repeat("rr", len(sizes)))
	b.WriteString("SZBD ")
	for _, s := range sizes {
		fmt.Fprintf(&b, "& \\multicolumn{2}{c}{%s} ", human(s))
	}
	b.WriteString("\\\\\n")
	for range sizes {
		b.WriteString("& bez ind. [MB] & z ind. [MB] ")
	}
	b.WriteString("\\\\\n\\midrule\n")
	for _, st := range Stores {
		b.WriteString(storeNames[st] + " ")
		for _, s := range sizes {
			for _, idx := range []bool{false, true} {
				if x, ok := last[key{s, st, idx}]; ok {
					b.WriteString("& " + num(mb(x.i("total_bytes"))) + " ")
				} else {
					b.WriteString("& -- ")
				}
			}
		}
		b.WriteString("\\\\\n")
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	return o.text("tables/storage.tex", b.String())
}

func (r *Report) writeOverhead(o *out) error {
	type key struct{ store, scenario, otel string }
	vals := map[key][]float64{}
	var scs []string
	for _, x := range r.scenarios {
		if x["phase"] != "overhead" || x.b("warmup") || x["error"] != "" {
			continue
		}
		k := key{x["store"], x["scenario"], x["otel"]}
		vals[k] = append(vals[k], x.f("e2e_ms"))
		if !slices.Contains(scs, x["scenario"]) {
			scs = append(scs, x["scenario"])
		}
	}
	if len(vals) == 0 {
		return nil
	}
	slices.Sort(scs)
	rows := [][]string{{"store", "scenario", "otel", "n", "e2e_mean_ms", "e2e_median_ms"}}
	var b strings.Builder
	b.WriteString("% generated by tbench report\n\\begin{tabular}{llrrr}\n\\toprule\nSZBD & scen. & bez śledzenia [ms] & ze śledzeniem [ms] & różnica [\\%] \\\\\n\\midrule\n")
	for _, st := range Stores {
		for _, sc := range scs {
			on, off := summarize(vals[key{st, sc, "on"}]), summarize(vals[key{st, sc, "off"}])
			if on.N == 0 || off.N == 0 {
				continue
			}
			rows = append(rows, []string{st, sc, "on", strconv.Itoa(on.N), g(on.Mean), g(on.Median)}, []string{st, sc, "off", strconv.Itoa(off.N), g(off.Mean), g(off.Median)})
			fmt.Fprintf(&b, "%s & %s & %s & %s & %s \\\\\n", storeNames[st], sc, num(off.Median), num(on.Median), num(100*(on.Median-off.Median)/off.Median))
		}
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	if err := o.csv("data/overhead_summary.csv", rows); err != nil {
		return err
	}
	return o.text("tables/overhead.tex", b.String())
}

func (r *Report) writeHypotheses(o *out) error {
	h1, h2 := r.H1(), r.H2()
	var b strings.Builder
	b.WriteString("% generated by tbench report\n\\begin{tabular}{llrrrr}\n\\toprule\nRozmiar & para & przewaga 1 kl. & przewaga max kl. & $\\rho$ Spearmana & rośnie? \\\\\n\\midrule\n")
	grow1 := 0
	for _, x := range h1 {
		yes := "nie"
		if x.Grows {
			yes, grow1 = "tak", grow1+1
		}
		fmt.Fprintf(&b, "%s & %s/%s & %s & %s & %s & %s \\\\\n", human(x.Tickets), storeNames[x.Pair.NoSQL], storeNames[x.Pair.SQL],
			num(x.Advantage[0]), num(x.Advantage[len(x.Advantage)-1]), num(x.Rho), yes)
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	if err := o.text("tables/h1.tex", b.String()); err != nil {
		return err
	}
	sizes := r.sizes()
	b.Reset()
	fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{ll%sr}\n\\toprule\nScen. & indeksy ", strings.Repeat("r", len(sizes)))
	for _, s := range sizes {
		fmt.Fprintf(&b, "& %s ", human(s))
	}
	b.WriteString("& rośnie? \\\\\n\\midrule\n")
	grow2 := 0
	for _, x := range h2 {
		yes := "nie"
		if x.Grows {
			yes, grow2 = "tak", grow2+1
		}
		idx := "bez"
		if x.Indexes {
			idx = "z"
		}
		fmt.Fprintf(&b, "%s & %s ", x.Scenario, idx)
		for _, s := range sizes {
			if v, ok := x.Spread[s]; ok {
				b.WriteString("& " + num(v) + " ")
			} else {
				b.WriteString("& -- ")
			}
		}
		b.WriteString("& " + yes + " \\\\\n")
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	if err := o.text("tables/h2.tex", b.String()); err != nil {
		return err
	}

	// Macros used in the text of the report.
	var m strings.Builder
	m.WriteString("% generated by tbench report\n")
	def := func(name, val string) { fmt.Fprintf(&m, "\\newcommand{\\%s}{%s}\n", name, val) }
	def("HOneCases", strconv.Itoa(len(h1)))
	def("HOneGrows", strconv.Itoa(grow1))
	def("HOneVerdict", verdict(grow1, len(h1)))
	def("HTwoCases", strconv.Itoa(len(h2)))
	def("HTwoGrows", strconv.Itoa(grow2))
	def("HTwoVerdict", verdict(grow2, len(h2)))
	var names []string
	for _, s := range sizes {
		names = append(names, human(s))
	}
	def("SizesList", strings.Join(names, ", "))
	// Counts describe the main matrix; the security experiment has its own section.
	countPhase := func(rows []record) int {
		n := 0
		for _, x := range rows {
			if x["phase"] != "security" {
				n++
			}
		}
		return n
	}
	def("ScenarioRuns", strconv.Itoa(countPhase(r.scenarios)))
	def("ContentionRuns", strconv.Itoa(countPhase(r.contention)))
	inconsistent := 0
	for _, c := range r.Contention() {
		if !c.Consistent {
			inconsistent++
		}
	}
	def("ContentionInconsistent", strconv.Itoa(inconsistent))
	if err := o.text("macros.tex", m.String()); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(map[string]any{"h1": h1, "h2": h2}, "", "  ")
	return o.text("data/hypotheses.json", string(js))
}
