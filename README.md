# ticketbench-cli

`tbench` is the command line client of ticketbench. It builds a benchmark
plan, sends it to `bench-runner` over gRPC, writes the streamed results to
CSV and turns them into LaTeX tables and pgfplots data. Part of
[ryczek02/ticketbench](https://github.com/ryczek02/ticketbench).

## Install

Download an archive from the releases page, or:

```
go install github.com/ryczek02/ticketbench-cli/cmd/tbench@latest
docker run --rm --network host ghcr.io/ryczek02/ticketbench-cli ping
```

## Commands

| Command | What it does |
|---|---|
| `tbench ping` | Runner version and the status, server version and strategies of every store |
| `tbench scenarios` | The 27 scenarios (24 CRUD + 3 JSON) |
| `tbench gen --tickets 1M` | Dataset layout and checksum without a database; `--csv dir` exports it |
| `tbench run --scenarios R1,R2 --reps 10` | Selected scenarios on one size, indexes off, on or both |
| `tbench contend --clients 1,10,100,1000` | Contention test for all booking strategies |
| `tbench matrix --config matrix.yaml` | The full plan: sizes x stores x scenarios x index modes, plans, contention, tracing overhead |
| `tbench plan --config matrix.yaml` | Print the steps of a matrix without running it |
| `tbench report results/<run> --out dir` | Summary CSVs, `.dat` files for pgfplots, `.tex` tables, hypothesis checks |

Global flags: `--runner` (default `localhost:7070`, env `TBENCH_RUNNER`) and
`--trace` (sample the root span, default true).

Sizes accept suffixes: `500k`, `1M`, `10M`.

## Output

```
results/<run id>/
  meta.json          config, versions of every store, trace id of every segment
  scenarios.csv      one row per repetition: server_ms, e2e_ms, prepare_ms, rows, trace_id
  contention.csv     one row per round: throughput, conflicts, p50/p95/p99 latency, consistency
  loads.csv          reset, bulk load and index build times, row counts, checksum
  stats.csv          storage size with and without indexes
  errors.csv         failed steps
  plans/<size>/<store>/indexes-on|off/<scenario>.txt|json
```

Rows are appended as they arrive, so an interrupted run keeps what it
measured. `report` accepts several result directories, so a single segment
can be re-run and merged.

## Tracing

Every segment (one dataset size on one store) is a separate trace. The CLI
starts the root span, the runner and the store continue it, and the database
driver spans hang below the store span. The trace id is written into every
result row, so a slow repetition can be opened in Jaeger with
`http://localhost:16686/trace/<trace_id>`.

To export the CLI span as well, point it at the collector:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 OTEL_EXPORTER_OTLP_INSECURE=true tbench matrix ...
```

The tracing overhead experiment repeats a few scenarios twice on the same
data. The first pass uses a sampled root span, the second an unsampled one
that keeps the trace id but records nothing. `report` writes the difference
to `tables/overhead.tex`.

## Hypotheses

`report` checks both hypotheses from the project brief:

- H1: with more concurrent clients, the throughput advantage of Valkey and
  MongoDB over PostgreSQL and MariaDB grows. For each size and pair it
  computes the throughput ratio at every client count and the Spearman
  correlation with the client count.
- H2: differences between engines grow with the number of records. For each
  scenario and index mode it compares the slowest to fastest ratio at the
  smallest and at the largest size.

## Development

```
go test ./...                     # unit tests, no Docker
goreleaser release --snapshot     # local cross-builds into dist/
```

CI runs golangci-lint, tests with the race detector and Go benchmarks. It
builds snapshot archives with GoReleaser on every push and a release on `v*`
tags, plus a multi-arch image `ghcr.io/ryczek02/ticketbench-cli`.

## Authors

Łukasz Ryczko, Łukasz Pacholec. MIT license.
