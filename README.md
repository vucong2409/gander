# gander

> A single-process, low-overhead latency diagnostic for Go: capture the execution
> trace + kernel/cgroup signals onto **one correlated timeline**, then **tell you
> what's wrong** — flight-recorder style, triggered on latency anomalies.

`gander` ("take a gander" — to look closely) fuses signals that today live in
separate tools — Go execution traces and kernel/cgroup data (CFS throttling, PSI)
— onto a single timeline and runs a deterministic rules engine over it. It targets
latency-sensitive services (pinned hot loops, matching engines, message
dispatchers) where one slow work-unit stalls everything behind it — the
head-of-line blocking that aggregate profilers can't show.

Requires **Go 1.25+** (`runtime/trace.FlightRecorder`).

## How it works

Two layers, pure Go + `/proc`/cgroup — no eBPF, no `perf`, no special privileges:

- **Layer 1 — collect + synthesize.** A heartbeat watchdog times each work-unit.
  When one exceeds its budget, gander snapshots a correlated *bundle*: the
  flight-recorder execution trace (the lead-up to the stall), cgroup `cpu.stat`
  (CFS throttling), PSI CPU pressure, and a goroutine dump — aligned onto one
  clock. `gander emit` renders the bundle as a single **Perfetto** timeline:
  goroutine lanes labelled by function, wake-up arrows, GC/STW ranges, and the
  kernel counters as tracks beside them.
- **Layer 2 — diagnose.** `gander diag` runs a 100%-deterministic rules engine
  over the bundle and prints scored findings: the missed budget, **the slowest
  work-unit pinned to its goroutine and block reason**, CFS throttling, CPU
  pressure, GC stop-the-world share, and a block-reason breakdown.

## Try it

```bash
go install github.com/vucong2409/gander@latest    # or: go build -o gander .

# 1. produce a bundle from a synthetic stall (writes bundles/<timestamp>/)
gander demo --stall-chan --budget=10ms

# 2. render the fused timeline, then open the .json at https://ui.perfetto.dev
gander emit bundles/<timestamp>

# 3. diagnose
gander diag bundles/<timestamp>
```

`gander diag` prints, for example:

```
[warn] a work-unit exceeded its latency budget
       work-unit ran 11ms (budget 10ms) — 1.1× over
[warn] the slowest work-unit and what blocked it
       the slowest work-unit ran on G1 main.main for 41ms; it spent 40ms blocked on "chan receive"
[info] wait time by block reason (summed across goroutines, excluding idle runtime)
       select 4282ms; chan receive 3430ms; sleep 492ms
```

## Embed in your service

Add gander to a real service in a few lines — the watchdog auto-captures a bundle
whenever a work-unit overruns its budget:

```go
import "github.com/vucong2409/gander/record"

r, _ := record.Start(record.Options{
    Budget:    10 * time.Millisecond,
    BundleDir: "/var/gander",
})
defer r.Stop()

for msg := range queue {
    end := r.Begin(ctx) // marks a work-unit (+ a trace region for goid-aware diagnosis)
    process(msg)
    end()
}
```

Then inspect the bundles it writes with `gander emit` / `gander diag`.

## Status

Working proof-of-concept. The collect → fuse → diagnose loop runs end-to-end on
the demo and embeds in a real service via `record`. cgroup throttling and PSI are
Linux-only (a no-op on other platforms; the trace and diagnosis still work). The
`record` API and the finding schema are not yet stable.

## License

TBD (Apache-2.0 or MIT).
