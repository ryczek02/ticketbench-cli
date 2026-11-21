package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ryczek02/ticketbench-cli/internal/plan"
	"github.com/ryczek02/ticketbench-cli/internal/report"
	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
	"github.com/ryczek02/ticketbench-contracts/pkg/datagen"
	"github.com/ryczek02/ticketbench-contracts/pkg/scenario"
	"github.com/spf13/cobra"
)

func scenariosCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "scenarios",
		Short: "List the benchmark scenarios",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := table(g.out)
			fmt.Fprintln(tw, "ID\tOP\tFIXTURE\tNAME")
			for _, d := range scenario.All {
				fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", d.ID, d.Op, d.Fixture, d.Name)
			}
			return tw.Flush()
		},
	}
}

func genCmd(g *globals) *cobra.Command {
	var tickets string
	var seed uint64
	var csvDir string
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Show the dataset layout and checksum, optionally export it as CSV",
		Example: `  tbench gen --tickets 1M
  tbench gen --tickets 500k --csv ./data`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := parseInts[int64](tickets)
			if err != nil || len(n) != 1 {
				return fmt.Errorf("--tickets: %v", err)
			}
			start := time.Now()
			d, err := datagen.New(datagen.Spec{Tickets: n[0], Seed: seed})
			if err != nil {
				return err
			}
			layout := time.Since(start)
			tw := table(g.out)
			counts := d.Counts()
			for _, t := range []string{"kategorie", "organizatorzy", "obiekty", "sektory", "miejsca", "wydarzenia", "cenniki", "klienci", "kody_rabatowe", "rezerwacje", "bilety"} {
				fmt.Fprintf(tw, "%s\t%d\n", t, counts[t])
			}
			fmt.Fprintf(tw, "sandbox events\t%d\n", len(d.Sandbox))
			fmt.Fprintf(tw, "layout time\t%s\n", layout.Round(time.Microsecond))
			start = time.Now()
			fmt.Fprintf(tw, "checksum\t%s\n", d.Checksum())
			fmt.Fprintf(tw, "checksum time\t%s\n", time.Since(start).Round(time.Millisecond))
			if err := tw.Flush(); err != nil {
				return err
			}
			if csvDir != "" {
				return exportCSV(d, csvDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tickets, "tickets", "500k", "dataset size in tickets (500k, 1M, 10M)")
	cmd.Flags().Uint64Var(&seed, "seed", 42, "generator seed")
	cmd.Flags().StringVar(&csvDir, "csv", "", "write bilety.csv, rezerwacje.csv, klienci.csv and wydarzenia.csv to this directory")
	return cmd
}

func exportCSV(d *datagen.Dataset, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	open := func(name string, head ...string) (*csv.Writer, func() error, error) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		w := csv.NewWriter(f)
		_ = w.Write(head)
		return w, func() error { w.Flush(); return f.Close() }, nil
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	ev, closeEv, err := open("wydarzenia.csv", "id", "nazwa", "obiekt_id", "data_rozpoczecia", "status", "gatunek")
	if err != nil {
		return err
	}
	for _, e := range d.Events {
		_ = ev.Write([]string{i(e.ID), e.Name, i(e.VenueID), e.StartsAt.Format(time.RFC3339), e.Status, e.Meta.Genre})
	}
	if err := closeEv(); err != nil {
		return err
	}
	kl, closeKl, err := open("klienci.csv", "id", "imie", "nazwisko", "email", "miasto")
	if err != nil {
		return err
	}
	for id := int64(1); id <= d.Customers; id++ {
		c := d.Customer(id)
		_ = kl.Write([]string{i(c.ID), c.FirstName, c.LastName, c.Email, c.City})
	}
	if err := closeKl(); err != nil {
		return err
	}
	bt, closeBt, err := open("bilety.csv", "id", "wydarzenie_id", "miejsce_id", "cena", "status", "rezerwacja_id")
	if err != nil {
		return err
	}
	rz, closeRz, err := open("rezerwacje.csv", "id", "klient_id", "utworzono", "status", "kwota")
	if err != nil {
		return err
	}
	for e := range d.Events {
		s := d.Sales(e)
		for _, t := range s.Tickets {
			_ = bt.Write([]string{i(t.ID), i(t.EventID), i(t.SeatID), f(t.Price), t.Status, i(t.ReservationID)})
		}
		for _, r := range s.Reservations {
			_ = rz.Write([]string{i(r.ID), i(r.CustomerID), r.CreatedAt.Format(time.RFC3339), r.Status, f(r.Amount)})
		}
	}
	if err := closeBt(); err != nil {
		return err
	}
	return closeRz()
}

type commonFlags struct {
	stores  string
	tickets string
	seed    uint64
	out     string
}

func (c *commonFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.stores, "stores", "postgres,mariadb,mongo,valkey", "stores to use")
	cmd.Flags().StringVar(&c.tickets, "tickets", "500k", "dataset size in tickets")
	cmd.Flags().Uint64Var(&c.seed, "seed", 42, "generator seed")
	cmd.Flags().StringVar(&c.out, "out", "", "results directory (default results/<run id>)")
}

func runCmd(g *globals) *cobra.Command {
	var c commonFlags
	var scen, indexes string
	var reps, warmup int32
	var load, explain, failFast bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run selected scenarios on one dataset size",
		Example: `  tbench run --stores postgres --tickets 500k --scenarios R1,R2 --reps 10
  tbench run --scenarios 'C*' --indexes both --explain`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := plan.Default()
			m.Overhead.Enabled = false
			m.Contention.Clients = nil
			m.Scenarios, m.Repetitions, m.Warmup, m.Timeout, m.Explain, m.Seed = scen, reps, warmup, timeout, explain, c.seed
			m.Stores = splitList(c.stores)
			sizes, err := parseInts[int64](c.tickets)
			if err != nil {
				return err
			}
			m.Sizes = sizes
			switch indexes {
			case "off", "on":
				m.IndexModes = []string{indexes}
			case "both":
				m.IndexModes = []string{"off", "on"}
			default:
				return fmt.Errorf("--indexes must be off, on or both")
			}
			segs, err := m.Segments()
			if err != nil {
				return err
			}
			if !load {
				segs = withoutLoad(segs)
			}
			_, err = g.execute(cmd.Context(), segs, runOptions{outDir: c.out, failFast: failFast, config: m})
			return err
		},
	}
	c.register(cmd)
	cmd.Flags().StringVar(&scen, "scenarios", "all", "scenario ids: all, crud, C*, R1,R2 ...")
	cmd.Flags().Int32Var(&reps, "reps", 5, "measured repetitions per scenario")
	cmd.Flags().Int32Var(&warmup, "warmup", 1, "warm-up repetitions (not reported)")
	cmd.Flags().StringVar(&indexes, "indexes", "both", "off, on or both")
	cmd.Flags().BoolVar(&load, "load", true, "reset and load the dataset first")
	cmd.Flags().BoolVar(&explain, "explain", false, "save query plans")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "stop at the first failing step")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "timeout of one scenario iteration")
	return cmd
}

// withoutLoad drops reset, load and the trailing reset so the command works
// on data that is already loaded.
func withoutLoad(segs []plan.Segment) []plan.Segment {
	for i := range segs {
		var keep []*tbv1.Step
		for _, s := range segs[i].Steps {
			switch s.Kind.(type) {
			case *tbv1.Step_Reset_, *tbv1.Step_Load:
				continue
			}
			keep = append(keep, s)
		}
		segs[i].Steps = keep
	}
	return segs
}

func contendCmd(g *globals) *cobra.Command {
	var c commonFlags
	var clients, strategies string
	var hot, rounds int32
	var load bool
	var ratio float64
	cmd := &cobra.Command{
		Use:   "contend",
		Short: "Run the seat contention test (N clients booking the same seats)",
		Example: `  tbench contend --clients 1,10,100,1000 --strategies all
  tbench contend --stores valkey --strategies lua,watch --load=false`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := plan.Default()
			m.Overhead.Enabled = false
			m.Scenarios, m.IndexModes, m.Explain, m.Seed = "R1", []string{"on"}, false, c.seed
			m.Stores = splitList(c.stores)
			var err error
			if m.Sizes, err = parseInts[int64](c.tickets); err != nil {
				return err
			}
			if m.Contention.Clients, err = parseInts[int32](clients); err != nil {
				return err
			}
			m.Contention.HotTickets, m.Contention.Rounds, m.Contention.Strategies, m.Contention.TraceRatio = hot, rounds, splitList(strategies), ratio
			segs, err := m.Segments()
			if err != nil {
				return err
			}
			for i := range segs {
				var keep []*tbv1.Step
				for _, s := range segs[i].Steps {
					switch s.Kind.(type) {
					case *tbv1.Step_Scenarios, *tbv1.Step_Explain, *tbv1.Step_Stats:
						continue
					case *tbv1.Step_Reset_, *tbv1.Step_Load, *tbv1.Step_Indexes:
						if !load {
							continue
						}
					}
					keep = append(keep, s)
				}
				segs[i].Steps = keep
			}
			_, err = g.execute(cmd.Context(), segs, runOptions{outDir: c.out, config: m})
			return err
		},
	}
	c.register(cmd)
	cmd.Flags().StringVar(&clients, "clients", "1,10,100,1000", "concurrent client counts")
	cmd.Flags().Int32Var(&hot, "hot", 500, "number of contested tickets")
	cmd.Flags().Int32Var(&rounds, "rounds", 3, "rounds per client count")
	cmd.Flags().StringVar(&strategies, "strategies", "all", "booking strategies: all, default or a list")
	cmd.Flags().BoolVar(&load, "load", true, "reset and load the dataset first")
	cmd.Flags().Float64Var(&ratio, "trace-ratio", 0.01, "fraction of booking attempts traced")
	return cmd
}

func matrixFromFlags(path, sizes, stores string) (plan.Matrix, error) {
	m := plan.Default()
	var err error
	if path != "" {
		if m, err = plan.LoadFile(path); err != nil {
			return m, err
		}
	}
	if sizes != "" {
		if m.Sizes, err = parseInts[int64](sizes); err != nil {
			return m, err
		}
	}
	if stores != "" {
		m.Stores = splitList(stores)
	}
	return m, m.Validate()
}

func matrixCmd(g *globals) *cobra.Command {
	var config, sizes, stores, out string
	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Run the full benchmark matrix (all sizes, stores, scenarios, index modes, contention)",
		Example: `  tbench matrix --config matrix.yaml
  tbench matrix --sizes 500k --stores postgres,valkey --out results/smoke`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := matrixFromFlags(config, sizes, stores)
			if err != nil {
				return err
			}
			segs, err := m.Segments()
			if err != nil {
				return err
			}
			_, err = g.execute(cmd.Context(), segs, runOptions{outDir: out, config: m})
			return err
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "matrix.yaml (defaults are used when empty)")
	cmd.Flags().StringVar(&sizes, "sizes", "", "override sizes, e.g. 500k,1M")
	cmd.Flags().StringVar(&stores, "stores", "", "override stores")
	cmd.Flags().StringVar(&out, "out", "", "results directory (default results/<run id>)")
	return cmd
}

func planCmd(g *globals) *cobra.Command {
	var config, sizes, stores string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Print the steps a matrix would run, without running them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := matrixFromFlags(config, sizes, stores)
			if err != nil {
				return err
			}
			segs, err := m.Segments()
			if err != nil {
				return err
			}
			fmt.Fprint(g.out, plan.Describe(segs))
			return nil
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "matrix.yaml")
	cmd.Flags().StringVar(&sizes, "sizes", "", "override sizes")
	cmd.Flags().StringVar(&stores, "stores", "", "override stores")
	return cmd
}

func reportCmd(g *globals) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:     "report <results dir>...",
		Short:   "Aggregate result CSVs into summary tables, pgfplots data and LaTeX tables",
		Args:    cobra.MinimumNArgs(1),
		Example: `  tbench report results/20261121-220000-ab12cd --out ../report/generated`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := report.Load(args...)
			if err != nil {
				return err
			}
			files, err := r.Write(out)
			if err != nil {
				return err
			}
			fmt.Fprintf(g.out, "wrote %d files to %s\n", len(files), out)
			for _, s := range r.Findings() {
				fmt.Fprintln(g.out, " ", s)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "report", "output directory")
	return cmd
}
