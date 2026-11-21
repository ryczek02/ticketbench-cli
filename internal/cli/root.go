// Package cli implements the tbench command line.
package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
	"github.com/ryczek02/ticketbench-contracts/pkg/telemetry"
	"github.com/ryczek02/ticketbench-contracts/pkg/version"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type globals struct {
	runner string
	trace  bool
	out    io.Writer
	errOut io.Writer
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	g := &globals{out: stdout, errOut: stderr}
	root := &cobra.Command{
		Use:           "tbench",
		Short:         "Benchmark PostgreSQL, MariaDB, MongoDB and Valkey on a ticket reservation workload",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&g.runner, "runner", envOr("TBENCH_RUNNER", "localhost:7070"), "bench-runner address (env TBENCH_RUNNER)")
	root.PersistentFlags().BoolVar(&g.trace, "trace", envOr("TBENCH_TRACE", "true") == "true", "sample the root span so the whole run is traced")
	root.AddCommand(
		versionCmd(g), pingCmd(g), genCmd(g), scenariosCmd(g),
		runCmd(g), contendCmd(g), matrixCmd(g), planCmd(g), reportCmd(g),
	)

	shutdown, err := telemetry.Setup(ctx, telemetry.Options{Service: "tbench", Version: version.Version, Ratio: 1, LogText: true, LogLevel: slog.LevelWarn})
	if err != nil {
		fmt.Fprintln(stderr, "telemetry:", err)
		return 1
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shutdown(sctx)
	}()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (g *globals) dial() (tbv1.RunnerServiceClient, func(), error) {
	conn, err := grpc.NewClient(g.runner, append(telemetry.GRPCDialOptions(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 20 * time.Second, PermitWithoutStream: true}),
	)...)
	if err != nil {
		return nil, nil, err
	}
	return tbv1.NewRunnerServiceClient(conn), func() { conn.Close() }, nil
}

// rootContext starts the span every segment hangs off. When sampled is
// false the span context carries the "not sampled" flag, so the runner and
// the stores propagate the trace id but record nothing.
func rootContext(ctx context.Context, name string, sampled bool) (context.Context, trace.Span) {
	if !sampled {
		var tid trace.TraceID
		var sid trace.SpanID
		_, _ = rand.Read(tid[:])
		_, _ = rand.Read(sid[:])
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, Remote: true})
		ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
	}
	return telemetry.Tracer("ticketbench/cli").Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))
}

func newRunID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + fmt.Sprintf("-%x", b)
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

func parseInts[T int32 | int64](s string) ([]T, error) {
	var out []T
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ReplaceAll(p, "_", ""))
		if p == "" {
			continue
		}
		mult := int64(1)
		switch {
		case strings.HasSuffix(p, "k"):
			mult, p = 1_000, strings.TrimSuffix(p, "k")
		case strings.HasSuffix(p, "M"), strings.HasSuffix(p, "m"):
			mult, p = 1_000_000, p[:len(p)-1]
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", p)
		}
		out = append(out, T(v*mult))
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func versionCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(g.out, "tbench", version.String())
			return nil
		},
	}
}

func pingCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check the runner and every store",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, done, err := g.dial()
			if err != nil {
				return err
			}
			defer done()
			ctx, span := rootContext(cmd.Context(), "tbench ping", g.trace)
			defer span.End()
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			res, err := c.Ping(ctx, &tbv1.PingRequest{})
			if err != nil {
				return fmt.Errorf("runner %s: %w", g.runner, err)
			}
			fmt.Fprintf(g.out, "runner   %s  %s\ntrace_id %s\n\n", g.runner, res.RunnerVersion, telemetry.TraceID(ctx))
			tw := table(g.out)
			fmt.Fprintln(tw, "STORE\tADDRESS\tSTATUS\tSERVER\tSTRATEGIES")
			bad := 0
			for _, s := range res.Stores {
				st := "ok"
				if !s.Healthy {
					st, bad = "down: "+s.Error, bad+1
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Address, st, s.ServerVersion, strings.Join(s.Strategies, ","))
			}
			tw.Flush()
			if bad > 0 {
				return fmt.Errorf("%d store(s) not healthy", bad)
			}
			return nil
		},
	}
}
