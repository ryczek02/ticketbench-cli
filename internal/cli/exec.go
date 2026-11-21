package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ryczek02/ticketbench-cli/internal/plan"
	"github.com/ryczek02/ticketbench-cli/internal/results"
	tbv1 "github.com/ryczek02/ticketbench-contracts/gen/ticketbench/v1"
	"github.com/ryczek02/ticketbench-contracts/pkg/telemetry"
	"github.com/ryczek02/ticketbench-contracts/pkg/version"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type runOptions struct {
	outDir   string
	failFast bool
	config   any
}

// execute sends every segment to the runner and writes the results.
func (g *globals) execute(ctx context.Context, segs []plan.Segment, opts runOptions) (string, error) {
	c, done, err := g.dial()
	if err != nil {
		return "", err
	}
	defer done()

	runID := newRunID()
	dir := opts.outDir
	if dir == "" {
		dir = filepath.Join("results", runID)
	}
	w, err := results.Open(dir)
	if err != nil {
		return "", err
	}
	defer w.Close()
	host, _ := os.Hostname()
	w.Meta = results.Meta{
		RunID: runID, Started: time.Now().UTC(), CLIVersion: version.String(), Runner: g.runner, Config: opts.config,
		Host: map[string]any{"hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU()},
	}

	// One span for the whole invocation; each segment is its own trace
	// linked to it, which keeps traces small enough for Jaeger.
	mctx, mspan := rootContext(ctx, "tbench run "+runID, g.trace)
	defer mspan.End()
	if ping, err := c.Ping(mctx, &tbv1.PingRequest{}); err == nil {
		for _, s := range ping.Stores {
			w.Meta.Stores = append(w.Meta.Stores, results.StoreVersion{Name: s.Name, Engine: s.Engine, ServerVersion: s.ServerVersion, BuildVersion: s.BuildVersion})
		}
	}
	fmt.Fprintf(g.errOut, "run %s -> %s (%d segments)\n", runID, dir, len(segs))

	var failed error
	for i, seg := range segs {
		sampled := g.trace && seg.Labels["otel"] != "off"
		sctx, span := rootContext(context.Background(), "tbench segment "+seg.Name, sampled)
		span.AddLink(trace.Link{SpanContext: mspan.SpanContext()})
		span.SetAttributes(attribute.String("tb.run_id", runID), attribute.String("tb.segment", seg.Name))
		meta := results.Segment{Name: seg.Name, TraceID: telemetry.TraceID(sctx), Labels: seg.Labels, Started: time.Now().UTC()}
		fmt.Fprintf(g.errOut, "[%d/%d] %s trace_id=%s\n", i+1, len(segs), seg.Name, meta.TraceID)
		err := g.stream(sctx, c, w, &tbv1.RunRequest{RunId: runID, Steps: seg.Steps, Labels: seg.Labels, FailFast: opts.failFast}, seg.Name)
		span.End()
		meta.Ended = time.Now().UTC()
		if err != nil {
			meta.Error = err.Error()
			failed = errors.Join(failed, fmt.Errorf("%s: %w", seg.Name, err))
			fmt.Fprintf(g.errOut, "  segment failed: %v\n", err)
		}
		w.Meta.Segments = append(w.Meta.Segments, meta)
		w.Meta.Ended = time.Now().UTC()
		if err := w.SaveMeta(); err != nil {
			return dir, err
		}
		if ctx.Err() != nil {
			return dir, ctx.Err()
		}
		if err != nil && opts.failFast {
			break
		}
	}
	fmt.Fprintf(g.errOut, "done: %d result rows, %d errors, results in %s\n", w.Rows, w.Errors, dir)
	return dir, failed
}

func (g *globals) stream(ctx context.Context, c tbv1.RunnerServiceClient, w *results.Writer, req *tbv1.RunRequest, seg string) error {
	st, err := c.Run(ctx, req)
	if err != nil {
		return err
	}
	var started time.Time
	var current string
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			if current != "" {
				fmt.Fprintf(g.errOut, "  %-40s %s\n", current, time.Since(started).Round(time.Millisecond))
			}
			return nil
		}
		if err != nil {
			return err
		}
		if p := ev.GetProgress(); p != nil {
			if current != "" {
				fmt.Fprintf(g.errOut, "  %-40s %s\n", current, time.Since(started).Round(time.Millisecond))
			}
			current, started = fmt.Sprintf("%d/%d %s", p.StepIndex, p.StepCount, p.Message), time.Now()
			continue
		}
		if e := ev.GetError(); e != nil {
			fmt.Fprintf(g.errOut, "  error in %s: %s\n", e.Meta.StepId, e.Message)
		}
		if s := ev.GetScenario(); s != nil && s.Error != "" {
			fmt.Fprintf(g.errOut, "  %s rep %d: %s\n", s.Scenario, s.Repetition, s.Error)
		}
		if err := w.Event(seg, ev); err != nil {
			return err
		}
	}
}
