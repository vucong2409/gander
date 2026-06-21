//go:build linux

package collect

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// readProcSample reads the process's cgroup v2 CPU throttle and PSI counters.
// ok is false when the cgroup v2 files aren't present (e.g. not containerized,
// or cgroup v1), in which case the sampler simply records nothing.
func readProcSample() (ProcSample, bool) {
	dir := cgroupDir()

	cpuStat, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return ProcSample{}, false
	}
	throttled, nr := parseCPUStat(string(cpuStat))

	var psi uint64
	if b, err := os.ReadFile(filepath.Join(dir, "cpu.pressure")); err == nil {
		psi = parsePSISome(string(b))
	}

	return ProcSample{
		WallUnixNano:        time.Now().UnixNano(),
		ThrottledUsec:       throttled,
		NrThrottled:         nr,
		CPUPressureSomeUsec: psi,
	}, true
}

// cgroupDir resolves the current process's cgroup v2 directory under the unified
// mount, falling back to the mount root (the common case inside a container).
func cgroupDir() string {
	const root = "/sys/fs/cgroup"
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return root
	}
	for _, line := range strings.Split(string(b), "\n") {
		// cgroup v2 has a single line of the form "0::/path".
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(root, rest)
		}
	}
	return root
}
