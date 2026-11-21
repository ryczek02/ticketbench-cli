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
	macros, _ := os.ReadFile(filepath.Join(dir, "macros.tex"))
	if !strings.Contains(string(macros), `\newcommand{\HOneVerdict}{potwierdzona}`) {
		t.Fatalf("macros:\n%s", macros)
	}
}
