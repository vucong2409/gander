# gander

> A single-process, low-overhead latency diagnostic for Go: capture the execution
> trace + runtime + kernel/cgroup signals onto **one correlated timeline**, then
> **tell you what's wrong** — flight-recorder style, triggered on latency anomalies.

`gander` ("take a gander" — to look closely) fuses signals that today live in
separate tools — execution traces, `runtime/metrics`, and kernel/cgroup data
(CFS throttling, PSI, run-delay) — into a single timeline and runs a deterministic
rules engine over it. It targets latency-sensitive services (pinned hot loops,
matching engines, message dispatchers) where one slow work-unit stalls everything
behind it — the head-of-line blocking that aggregate profilers can't show.

Requires **Go 1.25+** (`runtime/trace.FlightRecorder`).

## Status

**Proof-of-concept / pre-implementation.** The code in this repo is an early
skeleton. Not yet usable.

## Expected PoC

The PoC proves the **two-layer thesis end to end** on a synthetic stall, using only
**pure-Go + `/proc`/cgroup** signals (no eBPF, no perf, no special privileges).

- **Layer 1 — Collector + Synthesis.** On a latency-anomaly trigger (a heartbeat
  watchdog: in-flight work-unit over budget), snapshot a correlated *bundle*:
  flight-recorder execution trace, `runtime/metrics`, a goroutine dump, `/proc`
  schedstat, cgroup `cpu.stat`, and PSI — all normalized onto one monotonic clock
  and tagged by entity (goroutine / P / M / tid / cpu / cgroup).
- **Layer 2 — Diagnosis.** A 100%-deterministic rules engine reads the bundle and
  emits scored findings with remediation, plus a profile-guided instrumentation
  advisor (`hot-or-blocking ∩ ¬already-instrumented`).
- **Output.** A fused **Perfetto** trace, the native Go trace passed through for
  **gotraceui**, and a `findings.json`.

**Success:** a demo binary deliberately induces a GC mark-assist storm and CFS
throttling; with no manual capture, the watchdog auto-fires, the fused Perfetto
view shows the GC / throttle / run-delay tracks aligned to the stalled work-units,
and `findings.json` correctly names the cause.

## License

TBD (Apache-2.0 or MIT).
