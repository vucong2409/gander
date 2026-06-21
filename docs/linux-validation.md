# Validating the kernel-signal path on Linux

gander's cgroup-throttling and PSI findings (`cfs-throttling`, `cpu-pressure`)
only fire on Linux with real cgroup v2 data. This runbook induces CFS throttling
deterministically and confirms gander detects it. Everything else — the execution
trace and goid-aware diagnosis — works on any platform.

## Prerequisites

- A Linux host with cgroup v2 (the unified hierarchy at `/sys/fs/cgroup`; the
  default on modern distros). Check: `stat -fc %T /sys/fs/cgroup` prints
  `cgroup2fs`.
- Go 1.25+ on the box, or cross-compile elsewhere and copy the binary over:
  `GOOS=linux GOARCH=amd64 go build -o gander .`

## Induce throttling and capture

`throttled_usec` / `nr_throttled` only appear once a CPU **quota** is set, so the
demo must run inside a quota-limited cgroup. It burns ~8 ms of CPU per 10 ms-budget
work-unit; under a 20% quota the kernel throttles it mid-unit, units overrun the
budget, and the watchdog auto-captures a bundle.

### With systemd (simplest)

```bash
systemd-run --user --scope -p CPUQuota=20% \
  ./gander demo --work=8ms --budget=10ms --stall-every=0 --duration=20s --bundle-dir=/tmp/gander
```

`--stall-every=0` disables the synthetic chan/sleep stalls, so the *only* stall
source is throttling — a clean signal.

### Raw cgroup v2 (no systemd)

```bash
sudo mkdir /sys/fs/cgroup/gander
echo "20000 100000" | sudo tee /sys/fs/cgroup/gander/cpu.max    # 20ms per 100ms = 20%
echo $$            | sudo tee /sys/fs/cgroup/gander/cgroup.procs # move this shell in
./gander demo --work=8ms --budget=10ms --stall-every=0 --duration=20s --bundle-dir=/tmp/gander
```

(If `cpu.max` doesn't exist, the cpu controller isn't delegated to that subtree —
use the systemd method instead.)

## Confirm

```bash
gander diag /tmp/gander/<timestamp>
```

Expect a finding like:

```
[critical] cgroup CPU throttling during the window
           CFS-throttled 7 time(s) for 42.0ms — the cgroup CPU quota capped the process
```

Then open the fused view and check the counter track lines up with the stalls:

```bash
gander emit /tmp/gander/<timestamp>     # -> fused.json, open at https://ui.perfetto.dev
```

The `cgroup.cpu.throttled_usec` track should step up exactly under the stalled
`work-unit` slices. If PSI is enabled (`CONFIG_PSI=y`; some distros need `psi=1`
on the kernel cmdline) a `cpu-pressure` finding and a
`cgroup.cpu.pressure_some_usec` track appear too.

## Troubleshooting

| Symptom | Cause |
|---|---|
| No `cfs-throttling` finding; `proc.json` has samples but `throttled_usec` is 0 | No quota set — run inside the quota cgroup above. |
| `proc.json` is `[]` (empty) | cgroup v1, or PSI/cpu.stat not found. Confirm `stat -fc %T /sys/fs/cgroup` is `cgroup2fs`. |
| No `cpu-pressure` finding | PSI not enabled in the kernel — throttling detection still works. |
| No bundle at all | Nothing exceeded budget — lower `--budget` or raise `--work`. |
