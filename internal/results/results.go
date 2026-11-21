// Package results writes runner events to CSV files as they arrive, so a run
// that is interrupted keeps everything measured so far.
package results

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
)

// File names inside a run directory.
const (
	ScenariosCSV  = "scenarios.csv"
	LoadsCSV      = "loads.csv"
	ContentionCSV = "contention.csv"
	StatsCSV      = "stats.csv"
	ErrorsCSV     = "errors.csv"
	MetaJSON      = "meta.json"
	PlansDir      = "plans"
)

var headers = map[string][]string{
	ScenariosCSV: {"run_id", "segment", "phase", "otel", "step_id", "store", "tickets", "indexes", "scenario", "op",
		"repetition", "warmup", "server_ms", "prepare_ms", "e2e_ms", "rows", "timed_out", "error", "trace_id"},
	LoadsCSV: {"run_id", "segment", "store", "tickets", "kind", "server_ms", "checksum", "rows", "trace_id"},
	ContentionCSV: {"run_id", "segment", "store", "tickets", "indexes", "strategy", "clients", "hot_tickets", "round",
		"duration_ms", "attempts", "successes", "conflicts", "errors", "consistent", "throughput_per_s",
		"p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_ms", "trace_id"},
	StatsCSV:  {"run_id", "segment", "store", "tickets", "indexes", "data_bytes", "index_bytes", "total_bytes", "rows", "trace_id"},
	ErrorsCSV: {"run_id", "segment", "step_id", "store", "message", "trace_id"},
}

// Segment describes the RunRequest the events belong to.
type Segment struct {
	Name    string            `json:"name"`
	TraceID string            `json:"trace_id"`
	Labels  map[string]string `json:"labels"`
	Started time.Time         `json:"started"`
	Ended   time.Time         `json:"ended"`
	Error   string            `json:"error,omitempty"`
}

// Meta is written to meta.json.
type Meta struct {
	RunID      string         `json:"run_id"`
	Started    time.Time      `json:"started"`
	Ended      time.Time      `json:"ended"`
	CLIVersion string         `json:"cli_version"`
	Runner     string         `json:"runner"`
	Stores     []StoreVersion `json:"stores"`
	Config     any            `json:"config"`
	Segments   []Segment      `json:"segments"`
	Host       map[string]any `json:"host"`
}

type StoreVersion struct {
	Name, Engine, ServerVersion, BuildVersion string
}

// Writer appends events of one run directory.
type Writer struct {
	dir   string
	mu    sync.Mutex
	files map[string]*os.File
	csv   map[string]*csv.Writer
	Meta  Meta
	// Counters for the progress line.
	Rows, Errors int
}

// Open creates (or appends to) the run directory.
func Open(dir string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Join(dir, PlansDir), 0o755); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, files: map[string]*os.File{}, csv: map[string]*csv.Writer{}}
	for name, head := range headers {
		path := filepath.Join(dir, name)
		_, statErr := os.Stat(path)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		cw := csv.NewWriter(f)
		if os.IsNotExist(statErr) {
			_ = cw.Write(head)
			cw.Flush()
		}
		w.files[name], w.csv[name] = f, cw
	}
	return w, nil
}

// Dir returns the run directory.
func (w *Writer) Dir() string { return w.dir }

func ms(ns int64) string { return strconv.FormatFloat(float64(ns)/1e6, 'f', 4, 64) }

func b2s(b bool) string { return strconv.FormatBool(b) }

func i2s[T ~int64 | ~int32 | ~int](v T) string { return strconv.FormatInt(int64(v), 10) }

func rowsJSON(m map[string]int64) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (w *Writer) write(name string, rec []string) {
	cw := w.csv[name]
	_ = cw.Write(rec)
	cw.Flush()
}

// Event records one runner event.
func (w *Writer) Event(seg string, ev *tbv1.RunEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch e := ev.Event.(type) {
	case *tbv1.RunEvent_Scenario:
		s, m := e.Scenario, e.Scenario.Meta
		def, _ := scenario.Lookup(scenario.ID(s.Scenario))
		w.write(ScenariosCSV, []string{m.RunId, seg, m.Labels["phase"], m.Labels["otel"], m.StepId, m.Store, i2s(m.Tickets), b2s(m.Indexes),
			s.Scenario, string(def.Op), i2s(s.Repetition), b2s(s.Warmup), ms(s.ServerNs), ms(s.PrepareNs), ms(s.E2ENs),
			i2s(s.Rows), b2s(s.TimedOut), s.Error, m.TraceId})
		w.Rows++
		if s.Error != "" {
			w.Errors++
		}
	case *tbv1.RunEvent_Load:
		l, m := e.Load, e.Load.Meta
		w.write(LoadsCSV, []string{m.RunId, seg, m.Store, i2s(m.Tickets), l.Kind, ms(l.ServerNs), l.Checksum, rowsJSON(l.Rows), m.TraceId})
	case *tbv1.RunEvent_Contend:
		c, m, r := e.Contend, e.Contend.Meta, e.Contend.Round
		tput := 0.0
		if r.DurationNs > 0 {
			tput = float64(r.Successes) / (float64(r.DurationNs) / 1e9)
		}
		w.write(ContentionCSV, []string{m.RunId, seg, m.Store, i2s(m.Tickets), b2s(m.Indexes), c.Strategy, i2s(c.Clients), i2s(c.HotTickets),
			i2s(r.Round), ms(r.DurationNs), i2s(r.Attempts), i2s(r.Successes), i2s(r.Conflicts), i2s(r.Errors), b2s(r.Consistent),
			strconv.FormatFloat(tput, 'f', 2, 64), ms(r.P50Ns), ms(r.P95Ns), ms(r.P99Ns), ms(r.MaxNs), ms(r.MeanNs), m.TraceId})
		w.Rows++
	case *tbv1.RunEvent_Stats:
		s, m := e.Stats, e.Stats.Meta
		w.write(StatsCSV, []string{m.RunId, seg, m.Store, i2s(m.Tickets), b2s(m.Indexes), i2s(s.DataBytes), i2s(s.IndexBytes), i2s(s.TotalBytes), rowsJSON(s.Rows), m.TraceId})
	case *tbv1.RunEvent_Plan:
		p, m := e.Plan, e.Plan.Meta
		mode := "off"
		if m.Indexes {
			mode = "on"
		}
		dir := filepath.Join(w.dir, PlansDir, i2s(m.Tickets), m.Store, "indexes-"+mode)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		ext := ".txt"
		if p.Format == "json" {
			ext = ".json"
		}
		body := fmt.Sprintf("-- %s %s tickets=%d indexes=%s trace=%s\n-- %s\n%s", m.Store, p.Scenario, m.Tickets, mode, m.TraceId, p.Statement, p.Plan)
		if p.Format == "json" {
			body = p.Plan
			_ = os.WriteFile(filepath.Join(dir, p.Scenario+".statement.txt"), []byte(p.Statement+"\n"), 0o644)
		}
		return os.WriteFile(filepath.Join(dir, p.Scenario+ext), []byte(body), 0o644)
	case *tbv1.RunEvent_Error:
		m := e.Error.Meta
		w.write(ErrorsCSV, []string{m.RunId, seg, m.StepId, m.Store, e.Error.Message, m.TraceId})
		w.Errors++
	}
	return nil
}

// SaveMeta writes meta.json.
func (w *Writer) SaveMeta() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	sort.SliceStable(w.Meta.Segments, func(i, j int) bool { return w.Meta.Segments[i].Started.Before(w.Meta.Segments[j].Started) })
	b, err := json.MarshalIndent(w.Meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, MetaJSON), b, 0o644)
}

// Close flushes all files.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for name, f := range w.files {
		w.csv[name].Flush()
		_ = f.Close()
	}
	return nil
}
