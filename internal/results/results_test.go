package results

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
)

func TestWriter(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta := &tbv1.ResultMeta{RunId: "r1", Store: "postgres", Tickets: 1000, TraceId: "abc", Labels: map[string]string{"phase": "matrix"}}
	_ = w.Event("seg", &tbv1.RunEvent{Event: &tbv1.RunEvent_Scenario{Scenario: &tbv1.ScenarioResult{Meta: meta, Scenario: "R1", ServerNs: 1_500_000, E2ENs: 2_000_000, Rows: 3}}})
	_ = w.Event("seg", &tbv1.RunEvent{Event: &tbv1.RunEvent_Plan{Plan: &tbv1.PlanResult{Meta: meta, Scenario: "R1", Format: "text", Plan: "Seq Scan", Statement: "SELECT 1"}}})
	_ = w.Event("seg", &tbv1.RunEvent{Event: &tbv1.RunEvent_Contend{Contend: &tbv1.ContendResult{Meta: meta, Clients: 10, Strategy: "cond",
		Round: &tbv1.ContendRound{DurationNs: 500_000_000, Successes: 100, Consistent: true}}}})
	if err := w.SaveMeta(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	b, _ := os.ReadFile(filepath.Join(dir, ScenariosCSV))
	if !strings.Contains(string(b), "postgres,1000,false,R1,R,0,false,1.5000,0.0000,2.0000,3,false,,abc") {
		t.Fatalf("scenarios.csv:\n%s", b)
	}
	c, _ := os.ReadFile(filepath.Join(dir, ContentionCSV))
	if !strings.Contains(string(c), ",200.00,") {
		t.Fatalf("throughput not computed:\n%s", c)
	}
	if _, err := os.Stat(filepath.Join(dir, PlansDir, "1000", "postgres", "indexes-off", "R1.txt")); err != nil {
		t.Fatal(err)
	}
	// Reopening appends without a second header.
	w2, _ := Open(dir)
	w2.Close()
	b2, _ := os.ReadFile(filepath.Join(dir, ScenariosCSV))
	if strings.Count(string(b2), "run_id") != 1 {
		t.Fatal("header written twice")
	}
}
