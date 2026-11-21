package plan

import (
	"slices"
	"testing"

	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	bad := []func(*Matrix){
		func(m *Matrix) { m.Repetitions = 50 },
		func(m *Matrix) { m.Repetitions = 0 },
		func(m *Matrix) { m.Sizes = []int64{10} },
		func(m *Matrix) { m.Stores = []string{"oracle"} },
		func(m *Matrix) { m.IndexModes = []string{"maybe"} },
		func(m *Matrix) { m.Scenarios = "Z1" },
	}
	for i, f := range bad {
		m := Default()
		f(&m)
		if m.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestSegments(t *testing.T) {
	m := Default()
	segs, err := m.Segments()
	if err != nil {
		t.Fatal(err)
	}
	// 3 sizes x 4 stores, plus 2 overhead segments per store at 500k.
	if len(segs) != 12+8 {
		t.Fatalf("%d segments", len(segs))
	}
	first := segs[0]
	kinds := []string{}
	for _, s := range first.Steps {
		switch k := s.Kind.(type) {
		case *tbv1.Step_Scenarios:
			kinds = append(kinds, "scen")
			if k.Scenarios.IterationBase+int64(k.Scenarios.Warmup+k.Scenarios.Repetitions) > BaseIndexesOn && k.Scenarios.IterationBase == BaseIndexesOff {
				t.Fatal("index-off pass overlaps the index-on pass")
			}
		case *tbv1.Step_Indexes:
			kinds = append(kinds, "idx")
		case *tbv1.Step_Contend:
			kinds = append(kinds, "contend")
		}
	}
	if len(kinds) != 4 || kinds[0] != "scen" || kinds[1] != "idx" || kinds[3] != "contend" {
		t.Fatalf("unexpected step order %v", kinds)
	}
	// The dataset kept for the overhead runs is reset at the end.
	last := segs[2].Steps[len(segs[2].Steps)-1]
	if _, ok := last.Kind.(*tbv1.Step_Reset_); !ok || segs[2].Labels["otel"] != "off" {
		t.Fatal("overhead segment must end with a reset")
	}
}

func TestSecuritySegments(t *testing.T) {
	m := Default()
	off := false
	m.Main = &off
	m.Sizes = []int64{1_000_000}
	m.Security = Security{Enabled: true, Profiles: []string{"base", "role", "tls", "tls_role"}, Scenarios: "crud",
		Repetitions: 5, Warmup: 1, Clients: []int32{1, 100}, HotTickets: 500, Rounds: 3}
	segs, err := m.Segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 4 {
		t.Fatalf("%d segments, want one per store", len(segs))
	}
	var profiles []string
	bases := map[int64]bool{}
	for _, st := range segs[0].Steps {
		switch k := st.Kind.(type) {
		case *tbv1.Step_Scenarios:
			profiles = append(profiles, k.Scenarios.Profile)
			if bases[k.Scenarios.IterationBase] {
				t.Fatal("profiles share iterations")
			}
			bases[k.Scenarios.IterationBase] = true
		case *tbv1.Step_Contend:
			if len(k.Contend.Clients) != 2 {
				t.Fatal("contention clients")
			}
		}
	}
	// warm-up pass (base) + ABCD DCBA
	want := []string{"", "", "role", "tls", "tls_role", "tls_role", "tls", "role", ""}
	if !slices.Equal(profiles, want) {
		t.Fatalf("profiles %v, want %v", profiles, want)
	}
	m.Security.Profiles = []string{"bogus"}
	if m.Validate() == nil {
		t.Fatal("unknown profile accepted")
	}
}
