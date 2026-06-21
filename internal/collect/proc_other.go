//go:build !linux

package collect

// readProcSample is a no-op off Linux: cgroup CPU throttling and PSI are Linux
// features, so the sampler collects nothing and proc.json comes out empty.
func readProcSample() (ProcSample, bool) { return ProcSample{}, false }
