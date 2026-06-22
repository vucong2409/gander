# Capture triggers

gander's recording is **always on** once you call `record.Start` — the flight
recorder and the cgroup/PSI sampler run continuously, keeping a rolling window of
the recent past in memory. A *trigger* is just the thing that decides **when** to
freeze that window into a bundle on disk.

You choose the autonomous triggers with `Options.Triggers` (a bitflag — OR them
together). Two more ways to capture, `Snapshot` and `Handler`, are always
available no matter what `Triggers` is set to.

```go
r, _ := record.Start(record.Options{
    Triggers: record.OnBudget | record.OnSignal,
    Window:   5 * time.Second,
    Budget:   10 * time.Millisecond,
})
defer r.Stop()
```

## The look-back window

`Options.Window` sets how much execution-trace history the flight recorder keeps,
and therefore how far *before* the trigger each bundle reaches. A capture can only
show what was still in the buffer — you can't retroactively get more than `Window`
seconds. Larger windows cost more memory; the default (0) keeps the runtime's
default (a few seconds), which is usually enough to see a stall's lead-up.

## The triggers

### `OnBudget` — auto-capture on a latency miss (default)

A heartbeat watchdog times each work-unit you wrap with `Begin`/`end`. When one
stays in flight longer than `Budget`, it captures.

- **Config:** `Budget` (default 10ms).
- **Needs instrumentation:** yes — you must call `Begin(ctx)` / `end()`.
- **Overhead:** ~40–200 ns per work-unit (see the README overhead note).
- **Use when:** you can mark work-units and want hands-off capture exactly when a
  unit blows its latency budget.

### `OnSignal` — capture on a signal

Captures when the process receives `Options.Signal` (default `SIGUSR1`), so an
operator can grab the recent past on demand:

```sh
kill -USR1 <pid>
```

- **Config:** `Signal` (default `SIGUSR1`).
- **Needs instrumentation:** no.
- **Overhead:** none until fired.
- **Use when:** "capture it the next time it acts up" — zero code in the hot path.

### `Continuous` — rolling capture

Captures every `Interval`, keeping only the last `Keep` bundles on disk (older
ones are pruned after each capture). A flight recorder that also persists to disk.

- **Config:** `Interval` (default 30s), `Keep` (0 = keep everything).
- **Needs instrumentation:** no.
- **Overhead:** one bundle write per interval; bounded disk via `Keep`.
- **Use when:** you can't predict the bad moment and would rather never miss it.
- **Note:** `Cooldown` is still a floor — set `Interval` ≥ `Cooldown`.

### `Snapshot(reason)` — capture from your own code (always available)

```go
if resp.Status >= 500 { r.Snapshot("5xx on /checkout") }
```

You decide the condition (an error, a slow query, a queue-depth threshold, a
circuit-breaker trip); gander captures the window. Returns the bundle directory,
or `("", nil)` if debounced by `Cooldown`.

### `Handler()` — pull over HTTP (always available)

```go
mux.Handle("/debug/gander/", r.Handler())
// curl 'http://localhost:8080/debug/gander/?reason=manual'  -> {"bundle": "...", "reason": "manual"}
```

A GET captures a bundle and replies with its path as JSON — the `net/http/pprof`
model: attach and pull at runtime, no redeploy. Returns `429` if debounced.

## Cooldown

`Options.Cooldown` (default 1s) is the minimum interval between bundles across
**all** triggers, so a burst (a sustained stall, a tight `Continuous` interval, an
impatient `curl` loop) can't flood the disk. A debounced capture is a no-op:
`Snapshot` returns `("", nil)`, `Handler` returns `429`.

## Comparison

| Trigger | Who decides *when* | Hot-path instrumentation | Steady-state cost | Misses the event if…? |
|---|---|---|---|---|
| `OnBudget` | gander (budget breach) | `Begin`/`end` required | per-work-unit ns | budget set too loose |
| `OnSignal` | a human/operator | none | none until fired | nobody sends the signal in time |
| `Continuous` | the clock | none | a bundle per interval | the stall is shorter than the window gap *and* you keep too few |
| `Snapshot` | your code | none (you call it) | none until called | your condition doesn't fire |
| `Handler` | an HTTP caller | none | none until hit | nobody pulls in time |

They compose: a common setup is `OnBudget` for automatic latency-miss capture,
plus `OnSignal` or `Handler` as a manual escape hatch, plus `Continuous` with a
small `Keep` as a safety net.

## Inspecting what you captured

Every trigger writes the same bundle layout. Inspect it with:

```sh
gander emit <bundle-dir>   # fused Perfetto timeline (open the .json at ui.perfetto.dev)
gander diag <bundle-dir>   # deterministic findings
```

`meta.json` records which trigger fired (`trigger.source` is one of `heartbeat`,
`signal`, `continuous`, `manual`, `http`) and why.
