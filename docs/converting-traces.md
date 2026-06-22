# Converting a Go trace to the fused Perfetto view

`gander emit` doubles as a standalone converter: it turns **any Go 1.25 execution
trace** into the fused Perfetto timeline. It's a drop-in for gotraceui, whose
parser does not read Go 1.25 traces.

## Usage

```bash
gander emit <trace.bin>          # writes <trace>.fused.json next to it (default)
gander emit -o out.json <trace>  # explicit output path
gander emit -o - <trace> | ...   # write to stdout, for piping
```

Flags may appear before or after the path. Open the resulting `.json` at
<https://ui.perfetto.dev>.

## Any trace of the standard format works

The flight recorder, `runtime/trace`, `go test -trace`, and `net/http/pprof` all
emit the same wire format, so all of them convert:

```bash
# a gander capture bundle (or your own FlightRecorder.WriteTo output)
gander emit bundles/20260101T000000.000-123     # -> bundles/.../fused.json

# a runtime/trace.Start()/Stop() output file
gander emit trace.out                            # -> trace.fused.json

# a go test execution trace
go test -trace=t.bin ./...
gander emit t.bin                                # -> t.fused.json

# a live server's pprof trace
curl 'localhost:6060/debug/pprof/trace?seconds=5' -o p.bin
gander emit p.bin                                # -> p.fused.json
```

## What you get from a bare trace

| Layer | From a bare `.bin`? | Source |
|---|---|---|
| Goroutine lanes (states, block reasons, stacks) | yes | the trace |
| GC / stop-the-world ranges + global STW overlays | yes | the trace |
| User regions + slowest-work-unit stall marker | yes | the trace |
| Wake-up (causality) arrows | yes | the trace |
| CPU-sample markers | yes, if the trace was taken with CPU profiling on | the trace |
| Runtime / heap / scheduler counters | yes | embedded in the trace |
| **cgroup CFS-throttling / PSI counters** | **no** | gander's live proc sampler |

The cgroup/PSI layer can't be recovered from a trace alone — it only exists if
gander's proc sampler ran alongside the workload (a capture bundle via the
[`record`](../record) API or `gander demo`). Everything else is reconstructed
from the trace, so a bare `.bin` still gives you the full fused view.

You can also run the diagnosis layer on any trace:

```bash
gander diag bundles/20260101T000000.000-123      # scored findings + findings.json
```

(`diag` needs a bundle directory; the cgroup-throttling and missed-budget
findings depend on `proc.json` / `meta.json`, but the trace-derived findings —
the stalled work-unit, GC pressure, block reasons — work from `trace.bin` alone.)

## Why not gotraceui / `go tool trace`?

gotraceui's trace parser lags Go releases and does not read Go 1.25 traces; `go
tool trace`'s bundled viewer also breaks on recent Chrome. gander parses with
`golang.org/x/exp/trace`, which is current, and renders to Perfetto — so the 1.25
trace you can't open elsewhere opens here.
