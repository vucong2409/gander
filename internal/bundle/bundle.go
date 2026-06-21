// Package bundle defines the on-disk layout of a gander capture bundle and the
// metadata written alongside it.
//
// A bundle is a directory assembled when a latency anomaly fires. Over the
// course of the PoC it accumulates:
//
//	bundle/<ts>/
//	  meta.json        trigger reason, env, clock baseline (this package)
//	  goroutines.txt   pprof goroutine dump          (internal/collect)
//	  trace.bin        execution trace               (PR4)
//	  metrics.json     runtime/metrics samples       (PR5)
//	  proc.json        schedstat / cgroup / PSI       (PR6)
//	  fused.perfetto   fused timeline                 (PR9)
//	  findings.json    diagnosis                      (PR10)
package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
)

// SchemaVersion is the meta.json schema version. Bump it on breaking changes.
const SchemaVersion = 1

// procStart anchors a monotonic clock for the bundle's clock baseline. Reading
// it as time.Since(procStart) yields nanoseconds on a monotonic axis, which PR8
// will align with the execution trace's clock.
var procStart = time.Now()

// Trigger describes why a capture fired.
type Trigger struct {
	Reason string         `json:"reason"`           // human-readable, e.g. "work-unit exceeded budget"
	Source string         `json:"source"`           // e.g. "heartbeat"
	Detail map[string]any `json:"detail,omitempty"` // optional structured context (seq, elapsed, ...)
}

// Env records the runtime environment at capture time — enough to interpret the
// other artifacts later (cgroup CPU limits are added in PR6).
type Env struct {
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	NumCPU     int    `json:"num_cpu"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	GOMEMLIMIT int64  `json:"gomemlimit_bytes"` // math.MaxInt64 when unset
	GOGC       string `json:"gogc,omitempty"`
}

// ClockBaseline captures wall and monotonic clocks at the instant of capture so
// later stages can put heterogeneous sources on one axis (see PR8).
type ClockBaseline struct {
	WallUnixNano  int64 `json:"wall_unix_nano"`
	MonotonicNano int64 `json:"monotonic_nano"` // ns since process start
}

// Meta is the content of meta.json.
type Meta struct {
	SchemaVersion int           `json:"schema_version"`
	CreatedAt     string        `json:"created_at"` // RFC3339Nano
	Trigger       Trigger       `json:"trigger"`
	Env           Env           `json:"env"`
	Clock         ClockBaseline `json:"clock"`
}

// NewMeta builds a Meta for a capture triggered at instant at.
func NewMeta(t Trigger, at time.Time) Meta {
	return Meta{
		SchemaVersion: SchemaVersion,
		CreatedAt:     at.Format(time.RFC3339Nano),
		Trigger:       t,
		Env:           gatherEnv(),
		Clock: ClockBaseline{
			WallUnixNano:  at.UnixNano(),
			MonotonicNano: int64(time.Since(procStart)),
		},
	}
}

func gatherEnv() Env {
	return Env{
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		// A negative argument reads the current limit without changing it.
		GOMEMLIMIT: debug.SetMemoryLimit(-1),
		GOGC:       os.Getenv("GOGC"),
	}
}

// WriteMeta writes m to <dir>/meta.json.
func WriteMeta(dir string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644)
}

// CreateDir creates (and returns) a fresh, uniquely-named bundle directory under
// root, prefixed with the timestamp at. root is created if it does not exist.
func CreateDir(root string, at time.Time) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// MkdirTemp appends random characters, guaranteeing uniqueness even for
	// captures that land in the same millisecond.
	return os.MkdirTemp(root, at.Format("20060102T150405.000")+"-")
}
