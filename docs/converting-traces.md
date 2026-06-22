# Converting a Go trace to the fused Perfetto view

`gander emit` doubles as a standalone converter: it turns **any Go 1.25 execution
trace** into the fused Perfetto timeline. It's a stand-in for gotraceui, which
currently panics on Go 1.25 traces
([dominikh/gotraceui#184](https://github.com/dominikh/gotraceui/issues/184)).

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

## Why not gotraceui?

gotraceui currently panics on Go 1.25 traces
([dominikh/gotraceui#184](https://github.com/dominikh/gotraceui/issues/184),
likely fixed in a future release). The panic is in gotraceui's own `ptrace`
reconstruction layer; gander instead drives `golang.org/x/exp/trace`'s reader
directly with a thin event mapping, so the 1.25 traces it chokes on convert here
today. (gander has no dependency on gotraceui.)
