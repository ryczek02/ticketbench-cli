package report

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	s := summarize([]float64{1, 2, 3, 4})
	if s.Mean != 2.5 || s.Median != 2.5 || s.Min != 1 || s.Max != 4 || s.P95 != 4 {
		t.Fatalf("%+v", s)
	}
	if math.Abs(s.Std-1.2910) > 1e-3 {
		t.Fatalf("std %f", s.Std)
	}
	if summarize(nil).N != 0 {
		t.Fatal("empty sample")
	}
}

func TestSpearman(t *testing.T) {
	if r := spearman([]float64{1, 2, 3}, []float64{10, 20, 30}); math.Abs(r-1) > 1e-9 {
		t.Fatalf("rho %f", r)
	}
	if r := spearman([]float64{1, 2, 3}, []float64{3, 2, 1}); math.Abs(r+1) > 1e-9 {
		t.Fatalf("rho %f", r)
	}
}

func TestReport(t *testing.T) {
	r, err := Load("testdata/run")
	if err != nil {
		t.Fatal(err)
	}
	sc := r.Scenarios()
	pg := sc[ScenarioKey{1000, "postgres", false, "R1"}]
	if pg.Server.N != 2 || pg.Server.Mean != 3 {
		t.Fatalf("warm-up must be excluded: %+v", pg.Server)
	}
	if v := sc[ScenarioKey{2000, "valkey", false, "R4"}]; v.Timeouts != 1 || v.Server.N != 0 {
		t.Fatalf("timeout not counted: %+v", v)
	}
	h1 := r.H1()
	if len(h1) != 1 || !h1[0].Grows || h1[0].Advantage[0] != 2 || h1[0].Advantage[1] != 8 {
		t.Fatalf("H1 %+v", h1)
	}
	h2 := r.H2()
	if len(h2) != 1 || !h2[0].Grows || h2[0].Spread[2000] != 10 {
		t.Fatalf("H2 %+v", h2)
	}
	dir := t.TempDir()
	files, err := r.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("only %d files", len(files))
	}
	tab, _ := os.ReadFile(filepath.Join(dir, "tables/scenarios_2000.tex"))
	if !strings.Contains(string(tab), "t/o") || !strings.Contains(string(tab), `\textbf{\num{1.00}}`) {
		t.Fatalf("scenario table:\n%s", tab)
	}
	values, _ := os.ReadFile(filepath.Join(dir, "values.tex"))
	if !strings.Contains(string(values), `\@namedef{tb@s@postgres@1000@off@R1}{3}`) || !strings.Contains(string(values), `\@namedef{tb@c@valkey@1000@lua@100}{8000}`) {
		t.Fatalf("values.tex:\n%s", values)
	}
	macros, _ := os.ReadFile(filepath.Join(dir, "macros.tex"))
	if !strings.Contains(string(macros), `\newcommand{\HOneVerdict}{potwierdzona}`) {
		t.Fatalf("macros:\n%s", macros)
	}
}

func TestPlans(t *testing.T) {
	r, err := Load("testdata/run")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PlanSummary{}
	for _, p := range r.Plans() {
		got[p.Store+"/"+p.Scenario] = p
	}
	pg := got["postgres/R4"]
	if !pg.FullScan || pg.TimeMs != 12.5 || pg.Examined != 5000 || len(pg.Access) != 3 || pg.Access[1] != "Index Scan bilety_rezerwacja" || pg.Access[2] != "Bitmap Index Scan rezerwacje_utworzono" {
		t.Fatalf("postgres %+v", pg)
	}
	my := got["mariadb/R4"]
	if !my.FullScan || my.TimeMs != 26.5 || my.Examined != 1006 || my.Access[0] != "ALL r" || my.Access[1] != "ref bilety_rezerwacja" {
		t.Fatalf("mariadb %+v", my)
	}
	mo := got["mongo/R2"]
	if !mo.FullScan || mo.TimeMs != 40 || mo.Examined != 10000 || mo.Access[0] != "COLLSCAN" {
		t.Fatalf("mongo %+v", mo)
	}
	if v := got["valkey/R4"]; !v.FullScan {
		t.Fatalf("valkey %+v", v)
	}
	dir := t.TempDir()
	if _, err := r.Write(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tables/plans_postgres_mariadb_1000.tex")); err != nil {
		t.Fatal(err)
	}
}
