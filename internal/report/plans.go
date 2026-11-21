package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// PlanSummary condenses one saved query plan.
type PlanSummary struct {
	Tickets  int64
	Store    string
	Indexes  bool
	Scenario string
	// Access lists the access paths, e.g. "Seq Scan rezerwacje" or
	// "IXSCAN wydarzenie_id_1_status_1".
	Access []string
	// FullScan is true when any table or collection is read in full.
	FullScan bool
	// Examined is rows (MariaDB) or documents (MongoDB) read; -1 if unknown.
	Examined float64
	// TimeMs is the execution time reported by the engine; -1 if unknown.
	TimeMs float64
}

var (
	pgNode = regexp.MustCompile(`(Parallel )?(Seq Scan|Index Only Scan|Index Scan|Bitmap Index Scan|Bitmap Heap Scan)(?: Backward)?(?: using (\w+))? on (\w+)`)
	pgTime = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)
	pgRows = regexp.MustCompile(`Rows Removed by Filter: (\d+)`)
)

func summarizePG(body string) PlanSummary {
	s := PlanSummary{Examined: -1, TimeMs: -1}
	seen := map[string]bool{}
	for _, m := range pgNode.FindAllStringSubmatch(body, -1) {
		kind, idx, table := m[2], m[3], m[4]
		a := kind + " " + table
		switch {
		case kind == "Bitmap Index Scan":
			// "Bitmap Index Scan on <index>": the name after "on" is the index.
			a = kind + " " + strings.TrimPrefix(table, "idx_")
		case idx != "":
			a = kind + " " + strings.TrimPrefix(idx, "idx_")
		}
		if kind == "Seq Scan" {
			s.FullScan = true
		}
		if !seen[a] {
			seen[a] = true
			s.Access = append(s.Access, a)
		}
	}
	if m := pgTime.FindStringSubmatch(body); m != nil {
		s.TimeMs, _ = strconv.ParseFloat(m[1], 64)
	}
	var removed float64
	for _, m := range pgRows.FindAllStringSubmatch(body, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		removed += v
	}
	if removed > 0 {
		s.Examined = removed
	}
	return s
}

func summarizeMaria(body []byte) PlanSummary {
	s := PlanSummary{Examined: -1, TimeMs: -1}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return s
	}
	var walk func(any)
	var rows float64
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if name, ok := t["table_name"].(string); ok {
				access, _ := t["access_type"].(string)
				a := access + " " + name
				if key, ok := t["key"].(string); ok && key != "PRIMARY" {
					a = access + " " + strings.TrimPrefix(key, "idx_")
				} else if ok {
					a = access + " " + name + ".PK"
				}
				if access == "ALL" {
					s.FullScan = true
				}
				if !slices.Contains(s.Access, a) {
					s.Access = append(s.Access, a)
				}
				if r, ok := t["r_rows"].(float64); ok {
					loops := 1.0
					if l, ok := t["r_loops"].(float64); ok && l > 0 {
						loops = l
					}
					rows += r * loops
				}
			}
			if qb, ok := t["query_block"].(map[string]any); ok {
				if ms, ok := qb["r_total_time_ms"].(float64); ok {
					s.TimeMs = ms
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(doc)
	if rows > 0 {
		s.Examined = rows
	}
	return s
}

func summarizeMongo(body []byte) PlanSummary {
	s := PlanSummary{Examined: -1, TimeMs: -1}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return s
	}
	var find func(v any, key string) any
	find = func(v any, key string) any {
		switch t := v.(type) {
		case map[string]any:
			if x, ok := t[key]; ok {
				return x
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				if r := find(t[k], key); r != nil {
					return r
				}
			}
		case []any:
			for _, c := range t {
				if r := find(c, key); r != nil {
					return r
				}
			}
		}
		return nil
	}
	num := func(v any) float64 {
		switch t := v.(type) {
		case float64:
			return t
		case map[string]any: // extended JSON {"$numberLong": "123"}
			for _, k := range []string{"$numberLong", "$numberInt", "$numberDouble"} {
				if x, ok := t[k].(string); ok {
					f, _ := strconv.ParseFloat(x, 64)
					return f
				}
			}
		}
		return -1
	}
	plan := find(doc, "winningPlan")
	if m, ok := plan.(map[string]any); ok {
		if qp, ok := m["queryPlan"]; ok {
			plan = qp
		}
	}
	var stages func(any)
	stages = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if st, ok := m["stage"].(string); ok && (st == "COLLSCAN" || st == "IXSCAN" || st == "IDHACK" || st == "EXPRESS_IXSCAN" || st == "CLUSTERED_IXSCAN") {
			a := st
			if ix, ok := m["indexName"].(string); ok {
				a += " " + ix
			}
			if st == "COLLSCAN" {
				s.FullScan = true
			}
			if !slices.Contains(s.Access, a) {
				s.Access = append(s.Access, a)
			}
		}
		for _, k := range []string{"inputStage", "inputStages", "queryPlan"} {
			switch c := m[k].(type) {
			case map[string]any:
				stages(c)
			case []any:
				for _, x := range c {
					stages(x)
				}
			}
		}
	}
	stages(plan)
	if es := find(doc, "executionStats"); es != nil {
		if m, ok := es.(map[string]any); ok {
			s.Examined = num(m["totalDocsExamined"])
			if k := num(m["totalKeysExamined"]); k > s.Examined && len(s.Access) > 0 && !s.FullScan {
				s.Examined = k
			}
			s.TimeMs = num(m["executionTimeMillis"])
		}
	}
	return s
}

func summarizeValkey(body string) PlanSummary {
	s := PlanSummary{Examined: -1, TimeMs: -1}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	s.Access = []string{last}
	s.FullScan = strings.Contains(last, "SCAN r:*") || strings.Contains(last, "every event")
	return s
}

// Plans reads every saved plan of the result directories.
func (r *Report) Plans() []PlanSummary {
	var out []PlanSummary
	for _, dir := range r.dirs {
		root := filepath.Join(dir, "plans")
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, ".statement.txt") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) != 4 {
				return nil
			}
			tickets, _ := strconv.ParseInt(parts[0], 10, 64)
			store := parts[1]
			scen := strings.TrimSuffix(strings.TrimSuffix(parts[3], ".txt"), ".json")
			body, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var s PlanSummary
			switch store {
			case "postgres":
				s = summarizePG(string(body))
			case "mariadb":
				s = summarizeMaria(body)
			case "mongo":
				s = summarizeMongo(body)
			case "valkey":
				s = summarizeValkey(string(body))
			default:
				return nil
			}
			if len(s.Access) == 0 {
				return nil
			}
			s.Tickets, s.Store, s.Indexes, s.Scenario = tickets, store, parts[2] == "indexes-on", scen
			out = append(out, s)
			return nil
		})
	}
	slices.SortFunc(out, func(a, b PlanSummary) int {
		return strings.Compare(fmt.Sprint(a.Tickets, a.Scenario, slices.Index(Stores, a.Store), a.Indexes), fmt.Sprint(b.Tickets, b.Scenario, slices.Index(Stores, b.Store), b.Indexes))
	})
	return out
}

// planScenarios are the read-heavy scenarios shown in the plan tables.
var planScenarios = []string{"R1", "R2", "R3", "R4", "R5", "R6", "U3", "U6", "D1", "D4", "D6", "J1", "J2"}

func (r *Report) writePlans(o *out) error {
	plans := r.Plans()
	if len(plans) == 0 {
		return nil
	}
	rows := [][]string{{"tickets", "store", "indexes", "scenario", "access", "full_scan", "examined", "engine_time_ms"}}
	idx := map[string]PlanSummary{}
	for _, p := range plans {
		rows = append(rows, []string{strconv.FormatInt(p.Tickets, 10), p.Store, onOff(p.Indexes), p.Scenario,
			strings.Join(p.Access, "; "), strconv.FormatBool(p.FullScan), g(p.Examined), g(p.TimeMs)})
		idx[fmt.Sprint(p.Tickets, p.Store, p.Indexes, p.Scenario)] = p
	}
	if err := o.csv("data/plans_summary.csv", rows); err != nil {
		return err
	}
	cell := func(p PlanSummary, ok bool) string {
		if !ok {
			return "--"
		}
		acc := make([]string, 0, len(p.Access))
		for _, a := range p.Access {
			acc = append(acc, `\texttt{`+texEscape(a)+`}`)
		}
		c := strings.Join(acc, `\newline `)
		var extra []string
		if p.Examined >= 0 {
			extra = append(extra, num(p.Examined)+" wier.")
		}
		if p.TimeMs >= 0 {
			extra = append(extra, num(p.TimeMs)+" ms")
		}
		if len(extra) > 0 {
			c += `\newline{\color{gray}` + strings.Join(extra, ", ") + `}`
		}
		if p.FullScan {
			c = `\cellcolor{red!8}` + c
		}
		return c
	}
	for _, size := range r.sizes() {
		for _, group := range [][]string{{"postgres", "mariadb"}, {"mongo", "valkey"}} {
			var b strings.Builder
			fmt.Fprintf(&b, "%% generated by tbench report\n\\begin{tabular}{l%s}\n\\toprule\n", strings.Repeat(">{\\raggedright\\arraybackslash}p{3.45cm}", 4))
			b.WriteString("Scen. ")
			for _, st := range group {
				fmt.Fprintf(&b, "& \\multicolumn{2}{c}{%s} ", storeNames[st])
			}
			b.WriteString("\\\\\n")
			for range group {
				b.WriteString("& bez indeksów & z indeksami ")
			}
			b.WriteString("\\\\\n\\midrule\n")
			for _, sc := range planScenarios {
				b.WriteString(sc + " ")
				for _, st := range group {
					for _, on := range []bool{false, true} {
						p, ok := idx[fmt.Sprint(size, st, on, sc)]
						b.WriteString("& " + cell(p, ok) + " ")
					}
				}
				b.WriteString("\\\\\n")
			}
			b.WriteString("\\bottomrule\n\\end{tabular}\n")
			if err := o.text(fmt.Sprintf("tables/plans_%s_%d.tex", strings.Join(group, "_"), size), b.String()); err != nil {
				return err
			}
		}
	}
	// Share of plans with a full scan, per store and index mode.
	var b strings.Builder
	b.WriteString("% generated by tbench report\n\\makeatletter\n")
	for _, size := range r.sizes() {
		for _, st := range Stores {
			for _, on := range []bool{false, true} {
				full, all := 0, 0
				for _, sc := range planScenarios {
					if p, ok := idx[fmt.Sprint(size, st, on, sc)]; ok {
						all++
						if p.FullScan {
							full++
						}
					}
				}
				if all > 0 {
					fmt.Fprintf(&b, "\\@namedef{tb@f@%s@%d@%s}{%d}\n", st, size, onOff(on), full)
				}
			}
		}
	}
	fmt.Fprintf(&b, "\\makeatother\n\\newcommand{\\fullscans}[3]{\\tbget{f@#1@#2@#3}}\n\\newcommand{\\PlanScenarioCount}{%d}\n", len(planScenarios))
	return o.text("plans_values.tex", b.String())
}
