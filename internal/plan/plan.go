// Package plan turns a matrix configuration into runner steps.
package plan

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
	"github.com/ryczek02/ticketbench-contracts/pkg/datagen"
	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
	"gopkg.in/yaml.v3"
)

// Iteration layout inside one loaded dataset. Booking scenarios walk sandbox
// seats by iteration number, so passes must not overlap.
const (
	BaseIndexesOff   = 0
	BaseIndexesOn    = 50
	ExplainOffset    = 45
	MaxPassLength    = 44
	BaseOverheadOn   = 100
	BaseOverheadOff  = 300
	MaxOverheadReps  = 199
	defaultTimeout   = 60 * time.Second
	defaultHot       = 500
	defaultRounds    = 3
	defaultReps      = 5
	defaultWorkers   = 4
	defaultBatchSize = 5000
)

// Stores in the order used everywhere in the report.
var Stores = []string{"postgres", "mariadb", "mongo", "valkey"}

type Load struct {
	Workers   int `yaml:"workers"`
	BatchSize int `yaml:"batch_size"`
}

type Contention struct {
	Clients    []int32  `yaml:"clients"`
	HotTickets int32    `yaml:"hot_tickets"`
	Rounds     int32    `yaml:"rounds"`
	Strategies []string `yaml:"strategies"`
	// Sizes limits the contention test to some dataset sizes; empty = all.
	Sizes      []int64 `yaml:"sizes"`
	TraceRatio float64 `yaml:"trace_ratio"`
}

// Security runs the scenarios and the contention test through several
// connection profiles (least-privilege role, TLS) on the same data.
type Security struct {
	Enabled     bool     `yaml:"enabled"`
	Sizes       []int64  `yaml:"sizes"`
	Profiles    []string `yaml:"profiles"`
	Scenarios   string   `yaml:"scenarios"`
	Repetitions int32    `yaml:"repetitions"`
	Warmup      int32    `yaml:"warmup"`
	Clients     []int32  `yaml:"clients"`
	HotTickets  int32    `yaml:"hot_tickets"`
	Rounds      int32    `yaml:"rounds"`
}

// Profiles accepted by the security experiment ("base" is the default connection).
var Profiles = []string{"base", "role", "tls", "tls_role"}

type Overhead struct {
	Enabled     bool     `yaml:"enabled"`
	Size        int64    `yaml:"size"`
	Scenarios   []string `yaml:"scenarios"`
	Repetitions int32    `yaml:"repetitions"`
}

// Matrix is the content of matrix.yaml.
type Matrix struct {
	Seed        uint64        `yaml:"seed"`
	Sizes       []int64       `yaml:"sizes"`
	Stores      []string      `yaml:"stores"`
	Scenarios   string        `yaml:"scenarios"`
	Repetitions int32         `yaml:"repetitions"`
	Warmup      int32         `yaml:"warmup"`
	Timeout     time.Duration `yaml:"timeout"`
	IndexModes  []string      `yaml:"index_modes"`
	Explain     bool          `yaml:"explain"`
	Load        Load          `yaml:"load"`
	Contention  Contention    `yaml:"contention"`
	Overhead    Overhead      `yaml:"overhead"`
	Security    Security      `yaml:"security"`
	// Main runs the main matrix; false leaves only the security experiment.
	Main *bool `yaml:"main"`
}

func (m Matrix) mainEnabled() bool { return m.Main == nil || *m.Main }

// Default returns the configuration used for the report.
func Default() Matrix {
	return Matrix{
		Seed:        42,
		Sizes:       []int64{500_000, 1_000_000, 10_000_000},
		Stores:      Stores,
		Scenarios:   "all",
		Repetitions: defaultReps,
		Warmup:      1,
		Timeout:     defaultTimeout,
		IndexModes:  []string{"off", "on"},
		Explain:     true,
		Load:        Load{Workers: defaultWorkers, BatchSize: defaultBatchSize},
		Contention: Contention{
			Clients: []int32{1, 10, 100, 1000}, HotTickets: defaultHot, Rounds: defaultRounds,
			Strategies: []string{"all"}, TraceRatio: 0.01,
		},
		Overhead: Overhead{Enabled: true, Size: 500_000, Scenarios: []string{"R1", "R2", "R4", "U1", "U4", "C4"}, Repetitions: 30},
	}
}

// LoadFile reads a matrix file on top of the defaults.
func LoadFile(path string) (Matrix, error) {
	m := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, m.Validate()
}

// Validate checks limits that keep iterations inside their ranges.
func (m Matrix) Validate() error {
	if m.Warmup+m.Repetitions > MaxPassLength {
		return fmt.Errorf("warmup + repetitions must be <= %d, got %d", MaxPassLength, m.Warmup+m.Repetitions)
	}
	if m.Repetitions < 1 {
		return fmt.Errorf("repetitions must be >= 1")
	}
	if m.Overhead.Enabled && m.Overhead.Repetitions > MaxOverheadReps {
		return fmt.Errorf("overhead repetitions must be <= %d", MaxOverheadReps)
	}
	for _, s := range m.Sizes {
		if s < datagen.MinTickets {
			return fmt.Errorf("size %d below minimum %d", s, datagen.MinTickets)
		}
	}
	for _, st := range m.Stores {
		if !slices.Contains(Stores, st) {
			return fmt.Errorf("unknown store %q", st)
		}
	}
	for _, mode := range m.IndexModes {
		if mode != "off" && mode != "on" {
			return fmt.Errorf("index mode must be off or on, got %q", mode)
		}
	}
	if _, err := scenario.Parse(m.Scenarios); err != nil {
		return err
	}
	if m.Security.Enabled {
		for _, p := range m.Security.Profiles {
			if !slices.Contains(Profiles, p) {
				return fmt.Errorf("unknown security profile %q (have %v)", p, Profiles)
			}
		}
		if len(m.Security.Profiles)*int(m.Security.Warmup+m.Security.Repetitions) > 2*BaseIndexesOn {
			return fmt.Errorf("security: profiles x (warmup + repetitions) must be <= %d", 2*BaseIndexesOn)
		}
		if _, err := scenario.Parse(m.Security.Scenarios); err != nil {
			return err
		}
	}
	return nil
}

// Segment is one RunRequest: one dataset size on one store. Every segment
// gets its own trace.
type Segment struct {
	Name    string
	Store   string
	Tickets int64
	Labels  map[string]string
	Steps   []*tbv1.Step
}

func ids(list []scenario.ID) []string {
	out := make([]string, len(list))
	for i, id := range list {
		out[i] = string(id)
	}
	return out
}

type builder struct {
	store string
	ds    *tbv1.DatasetSpec
	steps []*tbv1.Step
}

func (b *builder) add(id string, kind func(*tbv1.Step)) {
	s := &tbv1.Step{Id: fmt.Sprintf("%s/%d/%02d-%s", b.store, b.ds.Tickets, len(b.steps)+1, id), Store: b.store, Dataset: b.ds}
	kind(s)
	b.steps = append(b.steps, s)
}

// Segments expands the matrix. Order: for each size, for each store.
func (m Matrix) Segments() ([]Segment, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var out []Segment
	if m.mainEnabled() {
		out = append(out, m.mainSegments()...)
	}
	if m.Security.Enabled {
		out = append(out, m.securitySegments()...)
	}
	return out, nil
}

func (m Matrix) mainSegments() []Segment {
	scs, _ := scenario.Parse(m.Scenarios)
	var out []Segment
	for _, size := range m.Sizes {
		for _, store := range m.Stores {
			b := &builder{store: store, ds: &tbv1.DatasetSpec{Tickets: size, Seed: m.Seed}}
			b.add("reset", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Reset_{Reset_: &tbv1.ResetStep{}} })
			b.add("load", func(s *tbv1.Step) {
				s.Kind = &tbv1.Step_Load{Load: &tbv1.LoadStep{Workers: int32(m.Load.Workers), BatchSize: int32(m.Load.BatchSize)}}
			})
			b.add("stats", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Stats{Stats: &tbv1.StatsStep{}} })
			indexes := false
			for _, mode := range m.IndexModes {
				on := mode == "on"
				if on != indexes {
					b.add("indexes-"+mode, func(s *tbv1.Step) { s.Kind = &tbv1.Step_Indexes{Indexes: &tbv1.IndexStep{Enabled: on}} })
					b.add("stats", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Stats{Stats: &tbv1.StatsStep{}} })
					indexes = on
				}
				base := int64(BaseIndexesOff)
				if on {
					base = BaseIndexesOn
				}
				b.add("scenarios-"+mode, func(s *tbv1.Step) {
					s.Kind = &tbv1.Step_Scenarios{Scenarios: &tbv1.ScenarioStep{
						Scenarios: ids(scs), Repetitions: m.Repetitions, Warmup: m.Warmup,
						IterationBase: base, TimeoutMs: m.Timeout.Milliseconds(),
					}}
				})
				if m.Explain {
					b.add("explain-"+mode, func(s *tbv1.Step) {
						s.Kind = &tbv1.Step_Explain{Explain: &tbv1.ExplainStep{Scenarios: ids(scs), Iteration: base + ExplainOffset}}
					})
				}
			}
			c := m.Contention
			if len(c.Clients) > 0 && (len(c.Sizes) == 0 || slices.Contains(c.Sizes, size)) {
				b.add("contend", func(s *tbv1.Step) {
					s.Kind = &tbv1.Step_Contend{Contend: &tbv1.ContendStep{
						Clients: c.Clients, HotTickets: c.HotTickets, Rounds: c.Rounds, Strategies: c.Strategies, TraceRatio: c.TraceRatio,
					}}
				})
			}
			if m.Overhead.Enabled && size == m.Overhead.Size {
				// Overhead runs on this data in separate segments, keep it.
			} else {
				b.add("reset", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Reset_{Reset_: &tbv1.ResetStep{}} })
			}
			out = append(out, Segment{
				Name: fmt.Sprintf("%s-%d", store, size), Store: store, Tickets: size,
				Labels: map[string]string{"phase": "matrix"}, Steps: b.steps,
			})
			if m.Overhead.Enabled && size == m.Overhead.Size {
				out = append(out, m.overheadSegments(store, b.ds)...)
			}
		}
	}
	return out
}

// overheadSegments repeat a few cheap scenarios with tracing on and off.
// The CLI starts the "otel=on" segment with a sampled root span and the
// "otel=off" segment with an unsampled one.
func (m Matrix) overheadSegments(store string, ds *tbv1.DatasetSpec) []Segment {
	var out []Segment
	for _, mode := range []struct {
		label string
		base  int64
	}{{"on", BaseOverheadOn}, {"off", BaseOverheadOff}} {
		b := &builder{store: store, ds: ds}
		b.add("overhead-otel-"+mode.label, func(s *tbv1.Step) {
			s.Kind = &tbv1.Step_Scenarios{Scenarios: &tbv1.ScenarioStep{
				Scenarios: m.Overhead.Scenarios, Repetitions: m.Overhead.Repetitions, Warmup: 2,
				IterationBase: mode.base, TimeoutMs: m.Timeout.Milliseconds(),
			}}
		})
		if mode.label == "off" {
			b.add("reset", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Reset_{Reset_: &tbv1.ResetStep{}} })
		}
		out = append(out, Segment{
			Name: fmt.Sprintf("%s-%d-otel-%s", store, ds.Tickets, mode.label), Store: store, Tickets: ds.Tickets,
			Labels: map[string]string{"phase": "overhead", "otel": mode.label}, Steps: b.steps,
		})
	}
	return out
}

// securitySegments load each store once (admin connection, indexes on) and
// then run the scenarios and the contention test through every profile.
func (m Matrix) securitySegments() []Segment {
	sec := m.Security
	sizes := sec.Sizes
	if len(sizes) == 0 {
		sizes = m.Sizes
	}
	scs, _ := scenario.Parse(sec.Scenarios)
	reps, warm := max(1, sec.Repetitions), sec.Warmup
	var out []Segment
	for _, size := range sizes {
		for _, store := range m.Stores {
			b := &builder{store: store, ds: &tbv1.DatasetSpec{Tickets: size, Seed: m.Seed}}
			b.add("reset", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Reset_{Reset_: &tbv1.ResetStep{}} })
			b.add("load", func(s *tbv1.Step) {
				s.Kind = &tbv1.Step_Load{Load: &tbv1.LoadStep{Workers: int32(m.Load.Workers), BatchSize: int32(m.Load.BatchSize)}}
			})
			b.add("indexes-on", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Indexes{Indexes: &tbv1.IndexStep{Enabled: true}} })
			for i, prof := range sec.Profiles {
				wire := prof
				if wire == "base" {
					wire = ""
				}
				base := int64(i) * int64(warm+reps)
				b.add("scenarios-"+prof, func(s *tbv1.Step) {
					s.Kind = &tbv1.Step_Scenarios{Scenarios: &tbv1.ScenarioStep{
						Scenarios: ids(scs), Repetitions: reps, Warmup: warm, IterationBase: base,
						TimeoutMs: m.Timeout.Milliseconds(), Profile: wire,
					}}
				})
				if len(sec.Clients) > 0 {
					b.add("contend-"+prof, func(s *tbv1.Step) {
						s.Kind = &tbv1.Step_Contend{Contend: &tbv1.ContendStep{
							Clients: sec.Clients, HotTickets: max(1, sec.HotTickets), Rounds: max(1, sec.Rounds), Profile: wire,
						}}
					})
				}
			}
			b.add("reset", func(s *tbv1.Step) { s.Kind = &tbv1.Step_Reset_{Reset_: &tbv1.ResetStep{}} })
			out = append(out, Segment{
				Name: fmt.Sprintf("%s-%d-security", store, size), Store: store, Tickets: size,
				Labels: map[string]string{"phase": "security"}, Steps: b.steps,
			})
		}
	}
	return out
}

// Describe prints the plan in a compact form.
func Describe(segs []Segment) string {
	var b strings.Builder
	var steps int
	for _, s := range segs {
		fmt.Fprintf(&b, "%s  (%s)\n", s.Name, labels(s.Labels))
		for _, st := range s.Steps {
			fmt.Fprintf(&b, "  %s\n", st.Id)
			steps++
		}
	}
	fmt.Fprintf(&b, "%d segments, %d steps\n", len(segs), steps)
	return b.String()
}

func labels(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, " ")
}
