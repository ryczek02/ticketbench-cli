package report

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
)

// SecurityProfiles in report order; "base" is the reference.
var SecurityProfiles = []string{"base", "role", "tls", "tls_role"}

var profileNames = map[string]string{"base": "bez TLS, admin", "role": "rola aplikacyjna", "tls": "TLS", "tls_role": "TLS + rola"}

var secOps = []string{"C", "R", "U", "D", "J", "all"}

// SecurityOverhead is the geometric mean of time(profile)/time(base) over
// the scenarios of one CRUD group.
type SecurityOverhead struct {
	Store, Profile, Op string
	Ratio              float64
	N                  int
}

// securityMeans returns the median server time per (store, profile,
// scenario). The median is robust to single slow repetitions, which matter
// for sub-millisecond operations.
func (r *Report) securityMeans() map[[3]string]float64 {
	a := map[[3]string][]float64{}
	for _, x := range r.scenarios {
		if x["phase"] != "security" || x.b("warmup") || x["error"] != "" || x.b("timed_out") {
			continue
		}
		prof := x["profile"]
		if prof == "" {
			prof = "base"
		}
		k := [3]string{x["store"], prof, x["scenario"]}
		a[k] = append(a[k], x.f("server_ms"))
	}
	out := map[[3]string]float64{}
	for k, v := range a {
		out[k] = summarize(v).Median
	}
	return out
}

// SecurityOverheads aggregates the security experiment.
func (r *Report) SecurityOverheads() []SecurityOverhead {
	means := r.securityMeans()
	var out []SecurityOverhead
	for _, st := range Stores {
		for _, prof := range SecurityProfiles[1:] {
			for _, op := range secOps {
				var logs []float64
				for _, def := range scenario.All {
					if op != "all" && string(def.Op) != op {
						continue
					}
					b, okb := means[[3]string{st, "base", string(def.ID)}]
					p, okp := means[[3]string{st, prof, string(def.ID)}]
					if okb && okp && b > 0 && p > 0 {
						logs = append(logs, math.Log(p/b))
					}
				}
				if len(logs) == 0 {
					continue
				}
				var s float64
				for _, l := range logs {
					s += l
				}
				out = append(out, SecurityOverhead{st, prof, op, math.Exp(s / float64(len(logs))), len(logs)})
			}
		}
	}
	return out
}

// SecurityNoise compares the two measured passes of the base profile
// (first and last in the counterbalanced order): the geometric mean of
// t_A/t_B over all scenarios, minus one. It is the difference between two
// runs of the same configuration, so profile overheads of that size cannot
// be told apart from run-to-run variation.
func (r *Report) SecurityNoise() map[string]float64 {
	type key struct{ store, scenario, step string }
	samples := map[key][]float64{}
	steps := map[[2]string][]string{}
	for _, x := range r.scenarios {
		if x["phase"] != "security" || x.b("warmup") || x["error"] != "" || x["profile"] != "base" {
			continue
		}
		k := key{x["store"], x["scenario"], x["step_id"]}
		samples[k] = append(samples[k], x.f("server_ms"))
		sk := [2]string{x["store"], x["scenario"]}
		if !slices.Contains(steps[sk], x["step_id"]) {
			steps[sk] = append(steps[sk], x["step_id"])
		}
	}
	logs := map[string][]float64{}
	for sk, ids := range steps {
		if len(ids) != 2 {
			continue
		}
		slices.Sort(ids)
		a := summarize(samples[key{sk[0], sk[1], ids[0]}]).Median
		b := summarize(samples[key{sk[0], sk[1], ids[1]}]).Median
		if a > 0 && b > 0 {
			logs[sk[0]] = append(logs[sk[0]], math.Log(a/b))
		}
	}
	out := map[string]float64{}
	for st, l := range logs {
		var s float64
		for _, v := range l {
			s += v
		}
		out[st] = math.Exp(s/float64(len(l))) - 1
	}
	return out
}

func (r *Report) securityContention() map[[3]string]float64 {
	type acc struct {
		sum float64
		n   int
	}
	a := map[[3]string]*acc{}
	for _, x := range r.contention {
		if x["phase"] != "security" {
			continue
		}
		k := [3]string{x["store"], x["profile"], x["clients"]}
		if a[k] == nil {
			a[k] = &acc{}
		}
		a[k].sum += x.f("throughput_per_s")
		a[k].n++
	}
	out := map[[3]string]float64{}
	for k, v := range a {
		out[k] = v.sum / float64(v.n)
	}
	return out
}

func pct(ratio float64) string {
	if math.IsNaN(ratio) {
		return "--"
	}
	return fmt.Sprintf(`\num[print-implicit-plus]{%+.1f}`, (ratio-1)*100)
}

func (r *Report) writeSecurity(o *out) error {
	means := r.securityMeans()
	if len(means) == 0 {
		return nil
	}
	rows := [][]string{{"store", "profile", "scenario", "median_ms", "base_median_ms", "ratio"}}
	for _, st := range Stores {
		for _, prof := range SecurityProfiles {
			for _, def := range scenario.All {
				p, ok := means[[3]string{st, prof, string(def.ID)}]
				if !ok {
					continue
				}
				b := means[[3]string{st, "base", string(def.ID)}]
				ratio := math.NaN()
				if b > 0 {
					ratio = p / b
				}
				rows = append(rows, []string{st, prof, string(def.ID), g(p), g(b), g(ratio)})
			}
		}
	}
	if err := o.csv("data/security_summary.csv", rows); err != nil {
		return err
	}
	ov := r.SecurityOverheads()
	idx := map[[3]string]SecurityOverhead{}
	for _, x := range ov {
		idx[[3]string{x.Store, x.Profile, x.Op}] = x
	}
	var b strings.Builder
	noise := r.SecurityNoise()
	b.WriteString("% generated by tbench report\n\\begin{tabular}{ll" + strings.Repeat("r", len(secOps)) + "r}\n\\toprule\n")
	b.WriteString("SZBD & profil & C & R & U & D & J & razem & base A/B \\\\\n\\midrule\n")
	for _, st := range Stores {
		for i, prof := range SecurityProfiles[1:] {
			name := ""
			if i == 0 {
				name = storeNames[st]
			}
			fmt.Fprintf(&b, "%s & %s ", name, profileNames[prof])
			for _, op := range secOps {
				x, ok := idx[[3]string{st, prof, op}]
				if !ok {
					b.WriteString("& -- ")
					continue
				}
				b.WriteString("& " + pct(x.Ratio) + " ")
			}
			if n, ok := noise[st]; ok && i == 0 {
				b.WriteString("& " + pct(1+n) + " ")
			} else {
				b.WriteString("& ")
			}
			b.WriteString("\\\\\n")
		}
		if st != Stores[len(Stores)-1] {
			b.WriteString("\\midrule\n")
		}
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	if err := o.text("tables/security.tex", b.String()); err != nil {
		return err
	}

	cont := r.securityContention()
	var clients []string
	for k := range cont {
		if !slices.Contains(clients, k[2]) {
			clients = append(clients, k[2])
		}
	}
	slices.SortFunc(clients, func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x - y
	})
	b.Reset()
	b.WriteString("% generated by tbench report\n\\begin{tabular}{l" + strings.Repeat("r", len(SecurityProfiles)*len(clients)) + "}\n\\toprule\n SZBD ")
	for _, prof := range SecurityProfiles {
		fmt.Fprintf(&b, "& \\multicolumn{%d}{c}{%s} ", len(clients), profileNames[prof])
	}
	b.WriteString("\\\\\n")
	for range SecurityProfiles {
		for _, cl := range clients {
			fmt.Fprintf(&b, "& %s kl. ", cl)
		}
	}
	b.WriteString("\\\\\n\\midrule\n")
	var vals strings.Builder
	vals.WriteString("% generated by tbench report\n\\makeatletter\n")
	for _, st := range Stores {
		b.WriteString(storeNames[st] + " ")
		for _, prof := range SecurityProfiles {
			for _, cl := range clients {
				v, ok := cont[[3]string{st, prof, cl}]
				if !ok {
					b.WriteString("& -- ")
					continue
				}
				b.WriteString("& " + num(v) + " ")
				fmt.Fprintf(&vals, "\\@namedef{tb@sc@%s@%s@%s}{%s}\n", st, prof, cl, g(v))
			}
		}
		b.WriteString("\\\\\n")
	}
	b.WriteString("\\bottomrule\n\\end{tabular}\n")
	if err := o.text("tables/security_contention.tex", b.String()); err != nil {
		return err
	}
	for _, x := range ov {
		fmt.Fprintf(&vals, "\\@namedef{tb@sp@%s@%s@%s}{%s}\n", x.Store, x.Profile, x.Op, g((x.Ratio-1)*100))
	}
	for _, st := range Stores {
		if n, ok := noise[st]; ok {
			fmt.Fprintf(&vals, "\\@namedef{tb@sn@%s}{%s}\n", st, g(n*100))
		}
	}
	vals.WriteString(`\makeatother
\newcommand{\secpct}[3]{\ifcsname tb@sp@#1@#2@#3\endcsname\num[round-mode=places,round-precision=1,print-implicit-plus]{\csname tb@sp@#1@#2@#3\endcsname}\,\%\else --\fi}
\newcommand{\seccval}[3]{\tbnum{sc@#1@#2@#3}}
\newcommand{\secnoise}[1]{\ifcsname tb@sn@#1\endcsname\num[round-mode=places,round-precision=1,print-implicit-plus]{\csname tb@sn@#1\endcsname}\,\%\else --\fi}
`)
	return o.text("security_values.tex", vals.String())
}
