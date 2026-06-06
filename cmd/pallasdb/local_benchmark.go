package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/teddymalhan/pallasdb/db"
)

const (
	defaultBenchmarkKeys         = 100_000
	defaultBenchmarkValueSize    = 128
	defaultBenchmarkKeySize      = 16
	defaultBenchmarkBatchSize    = 1_000
	defaultBenchmarkReadOps      = 100_000
	defaultBenchmarkLogThreshold = 1_000
	benchmarkSeed                = uint64(0x9e3779b97f4a7c15)
)

type localBenchmarkOptions struct {
	keys          int
	valueSize     int
	keySize       int
	batchSize     int
	readOps       int
	scanLimit     int
	logThreshold  int
	growthFactor  float32
	autoCompact   bool
	compact       bool
	cacheBytes    int64
	cacheCounters int64
	reset         bool
	format        string
	output        string
}

type benchmarkParams struct {
	DataDir       string  `json:"data_dir"`
	Keys          int     `json:"keys"`
	ValueSize     int     `json:"value_size"`
	KeySize       int     `json:"key_size"`
	BatchSize     int     `json:"batch_size"`
	ReadOps       int     `json:"read_ops"`
	ScanLimit     int     `json:"scan_limit"`
	LogThreshold  int     `json:"log_threshold"`
	GrowthFactor  float32 `json:"growth_factor"`
	AutoCompact   bool    `json:"auto_compact"`
	Compact       bool    `json:"compact"`
	CacheBytes    int64   `json:"cache_bytes"`
	CacheCounters int64   `json:"cache_counters"`
	Reset         bool    `json:"reset"`
}

type benchmarkEnvironment struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GoVersion  string `json:"go_version"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	NumCPU     int    `json:"num_cpu"`
}

type benchmarkMemDelta struct {
	Mallocs        uint64 `json:"mallocs"`
	TotalAlloc     uint64 `json:"total_alloc_bytes"`
	HeapAllocDelta int64  `json:"heap_alloc_delta_bytes"`
	NumGC          uint32 `json:"num_gc"`
}

type benchmarkPhase struct {
	Name         string            `json:"name"`
	Elapsed      string            `json:"elapsed"`
	ElapsedNanos int64             `json:"elapsed_ns"`
	Operations   int64             `json:"operations"`
	NSPerOp      float64           `json:"ns_per_op"`
	OpsPerSec    float64           `json:"ops_per_sec"`
	LogicalBytes int64             `json:"logical_bytes"`
	BytesPerSec  float64           `json:"bytes_per_sec"`
	Found        int64             `json:"found"`
	Missing      int64             `json:"missing"`
	Updated      int64             `json:"updated"`
	Errors       int64             `json:"errors"`
	Checksum     uint64            `json:"checksum"`
	Memory       benchmarkMemDelta `json:"memory"`
}

type benchmarkReport struct {
	StartedAt        string               `json:"started_at"`
	Command          string               `json:"command"`
	Parameters       benchmarkParams      `json:"parameters"`
	Environment      benchmarkEnvironment `json:"environment"`
	Phases           []benchmarkPhase     `json:"phases"`
	DataDirSizeBytes int64                `json:"data_dir_size_bytes"`
}

func newLocalBenchmarkCommand(_ *rootOptions, local *localOptions) *cobra.Command {
	opts := localBenchmarkOptions{
		keys:         defaultBenchmarkKeys,
		valueSize:    defaultBenchmarkValueSize,
		keySize:      defaultBenchmarkKeySize,
		batchSize:    defaultBenchmarkBatchSize,
		readOps:      defaultBenchmarkReadOps,
		logThreshold: defaultBenchmarkLogThreshold,
		growthFactor: 2,
		format:       "text",
	}
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Run a local disk-backed PallasDB benchmark",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := opts.validate(local.dataDir); err != nil {
				return err
			}
			report, err := runLocalBenchmark(local.dataDir, opts)
			if err != nil {
				return err
			}

			var out bytes.Buffer
			if err := writeBenchmarkReport(&out, report, opts.format); err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(out.Bytes()); err != nil {
				return err
			}
			if opts.output != "" {
				if err := appendBenchmarkOutput(opts.output, out.Bytes()); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&opts.keys, "keys", opts.keys, "number of keys to populate")
	cmd.Flags().IntVar(&opts.valueSize, "value-size", opts.valueSize, "bytes per value")
	cmd.Flags().IntVar(&opts.keySize, "key-size", opts.keySize, "fixed-width decimal key size")
	cmd.Flags().IntVar(&opts.batchSize, "batch-size", opts.batchSize, "writes per transaction commit")
	cmd.Flags().IntVar(&opts.readOps, "read-ops", opts.readOps, "random point reads after populate")
	cmd.Flags().IntVar(&opts.scanLimit, "scan-limit", opts.scanLimit, "maximum keys per iteration phase; 0 scans all keys")
	cmd.Flags().IntVar(&opts.logThreshold, "log-threshold", opts.logThreshold, "PallasDB memtable flush threshold")
	cmd.Flags().Float32Var(&opts.growthFactor, "growth-factor", opts.growthFactor, "PallasDB compaction growth factor")
	cmd.Flags().BoolVar(&opts.autoCompact, "auto-compact", opts.autoCompact, "enable background compaction during populate")
	cmd.Flags().BoolVar(&opts.compact, "compact", opts.compact, "run foreground compaction after populate")
	cmd.Flags().Int64Var(&opts.cacheBytes, "cache-bytes", opts.cacheBytes, "cache max cost bytes for cached random read; 0 disables cache")
	cmd.Flags().Int64Var(&opts.cacheCounters, "cache-counters", opts.cacheCounters, "cache counters for cached random read; 0 disables cache")
	cmd.Flags().BoolVar(&opts.reset, "reset", opts.reset, "remove the selected data directory before populating")
	cmd.Flags().StringVar(&opts.format, "format", opts.format, "report format: text or json")
	cmd.Flags().StringVar(&opts.output, "output", opts.output, "optional file path to append the complete report")
	return cmd
}

func (opts localBenchmarkOptions) validate(dataDir string) error {
	if dataDir == "" {
		return errors.New("--data-dir is required")
	}
	if opts.keys <= 0 {
		return errors.New("keys must be positive")
	}
	if opts.valueSize <= 0 {
		return errors.New("value-size must be positive")
	}
	if opts.keySize <= 0 {
		return errors.New("key-size must be positive")
	}
	if decimalDigits(opts.keys-1) > opts.keySize {
		return fmt.Errorf("key-size %d is too small for %d keys", opts.keySize, opts.keys)
	}
	if opts.batchSize <= 0 {
		return errors.New("batch-size must be positive")
	}
	if opts.readOps < 0 {
		return errors.New("read-ops must be non-negative")
	}
	if opts.scanLimit < 0 {
		return errors.New("scan-limit must be non-negative")
	}
	if opts.logThreshold <= 0 {
		return errors.New("log-threshold must be positive")
	}
	if opts.growthFactor < 2 {
		return errors.New("growth-factor must be at least 2")
	}
	if (opts.cacheBytes == 0) != (opts.cacheCounters == 0) {
		return errors.New("cache-bytes and cache-counters must be provided together")
	}
	if opts.cacheBytes < 0 {
		return errors.New("cache-bytes must be non-negative")
	}
	if opts.cacheCounters < 0 {
		return errors.New("cache-counters must be non-negative")
	}
	switch opts.format {
	case "text", "json":
	default:
		return fmt.Errorf("invalid format %q", opts.format)
	}
	if opts.reset {
		if err := validateResetDataDir(dataDir); err != nil {
			return err
		}
	}
	return nil
}

func validateResetDataDir(dataDir string) error {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	vol := filepath.VolumeName(abs)
	if abs == vol+string(os.PathSeparator) {
		return fmt.Errorf("refusing to reset filesystem root %q", abs)
	}
	if home, err := os.UserHomeDir(); err == nil && abs == home {
		return fmt.Errorf("refusing to reset home directory %q", abs)
	}
	return nil
}

func runLocalBenchmark(dataDir string, opts localBenchmarkOptions) (_ benchmarkReport, err error) {
	if opts.reset {
		if err := os.RemoveAll(dataDir); err != nil {
			return benchmarkReport{}, fmt.Errorf("reset data directory: %w", err)
		}
	}

	report := benchmarkReport{
		StartedAt:   time.Now().Format(time.RFC3339Nano),
		Command:     benchmarkCommandLine(),
		Parameters:  opts.params(dataDir),
		Environment: benchmarkEnvironment{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0), NumCPU: runtime.NumCPU()},
	}

	store, err := openBenchmarkStore(dataDir, opts, false)
	if err != nil {
		return benchmarkReport{}, fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close database: %w", closeErr)
		}
	}()

	phase, err := measureBenchmarkPhase("populate", func(m *benchmarkPhase) error {
		updated, err := populateBenchmark(store, opts, m)
		m.Updated = updated
		return err
	})
	report.Phases = append(report.Phases, phase)
	if err != nil {
		return benchmarkReport{}, err
	}
	if phase.Operations != int64(opts.keys) {
		return benchmarkReport{}, fmt.Errorf("populate wrote %d keys, want %d", phase.Operations, opts.keys)
	}

	if opts.compact {
		phase, err = measureBenchmarkPhase("compact", func(m *benchmarkPhase) error {
			m.Operations = 1
			return store.Compact()
		})
		report.Phases = append(report.Phases, phase)
		if err != nil {
			return benchmarkReport{}, fmt.Errorf("compact database: %w", err)
		}
	}

	phase, err = measureBenchmarkPhase("reopen", func(m *benchmarkPhase) error {
		m.Operations = 1
		if err := store.Close(); err != nil {
			return err
		}
		store, err = openBenchmarkStore(dataDir, opts, false)
		return err
	})
	report.Phases = append(report.Phases, phase)
	if err != nil {
		return benchmarkReport{}, fmt.Errorf("reopen database: %w", err)
	}

	phase, err = measureBenchmarkPhase("random_read", func(m *benchmarkPhase) error {
		return randomReadBenchmark(store, opts, m)
	})
	report.Phases = append(report.Phases, phase)
	if err != nil {
		return benchmarkReport{}, err
	}
	if phase.Found != int64(opts.readOps) {
		return benchmarkReport{}, fmt.Errorf("random_read found %d keys, want %d", phase.Found, opts.readOps)
	}

	expectedScan := expectedBenchmarkScanCount(opts)
	phase, err = measureBenchmarkPhase("iterate_keys", func(m *benchmarkPhase) error {
		return iterateBenchmark(store, opts, false, m)
	})
	report.Phases = append(report.Phases, phase)
	if err != nil {
		return benchmarkReport{}, err
	}
	if phase.Operations != int64(expectedScan) {
		return benchmarkReport{}, fmt.Errorf("iterate_keys scanned %d keys, want %d", phase.Operations, expectedScan)
	}

	phase, err = measureBenchmarkPhase("iterate_values", func(m *benchmarkPhase) error {
		return iterateBenchmark(store, opts, true, m)
	})
	report.Phases = append(report.Phases, phase)
	if err != nil {
		return benchmarkReport{}, err
	}
	if phase.Operations != int64(expectedScan) {
		return benchmarkReport{}, fmt.Errorf("iterate_values scanned %d keys, want %d", phase.Operations, expectedScan)
	}

	if opts.cacheBytes > 0 {
		if err := store.Close(); err != nil {
			return benchmarkReport{}, fmt.Errorf("close before cached read: %w", err)
		}
		store, err = openBenchmarkStore(dataDir, opts, true)
		if err != nil {
			return benchmarkReport{}, fmt.Errorf("open cached database: %w", err)
		}
		if err := warmBenchmarkCache(store, opts); err != nil {
			return benchmarkReport{}, err
		}
		phase, err = measureBenchmarkPhase("cached_random_read", func(m *benchmarkPhase) error {
			return randomReadBenchmark(store, opts, m)
		})
		report.Phases = append(report.Phases, phase)
		if err != nil {
			return benchmarkReport{}, err
		}
		if phase.Found != int64(opts.readOps) {
			return benchmarkReport{}, fmt.Errorf("cached_random_read found %d keys, want %d", phase.Found, opts.readOps)
		}
	}

	size, err := directorySize(dataDir)
	if err != nil {
		return benchmarkReport{}, fmt.Errorf("measure data directory: %w", err)
	}
	report.DataDirSizeBytes = size
	return report, nil
}

func (opts localBenchmarkOptions) params(dataDir string) benchmarkParams {
	return benchmarkParams{
		DataDir:       dataDir,
		Keys:          opts.keys,
		ValueSize:     opts.valueSize,
		KeySize:       opts.keySize,
		BatchSize:     opts.batchSize,
		ReadOps:       opts.readOps,
		ScanLimit:     opts.scanLimit,
		LogThreshold:  opts.logThreshold,
		GrowthFactor:  opts.growthFactor,
		AutoCompact:   opts.autoCompact,
		Compact:       opts.compact,
		CacheBytes:    opts.cacheBytes,
		CacheCounters: opts.cacheCounters,
		Reset:         opts.reset,
	}
}

func openBenchmarkStore(dataDir string, opts localBenchmarkOptions, cache bool) (*db.KV, error) {
	kvOpts := []db.KVOption{
		db.WithLogThreshold(opts.logThreshold),
		db.WithGrowthFactor(opts.growthFactor),
		db.WithAutoCompact(opts.autoCompact),
	}
	if cache {
		kvOpts = append(kvOpts, db.WithCache(opts.cacheBytes, opts.cacheCounters))
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return db.NewKV(dataDir, kvOpts...)
}

func measureBenchmarkPhase(name string, run func(*benchmarkPhase) error) (benchmarkPhase, error) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	phase := benchmarkPhase{Name: name}
	start := time.Now()
	err := run(&phase)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	phase.Elapsed = elapsed.String()
	phase.ElapsedNanos = elapsed.Nanoseconds()
	if phase.Operations > 0 {
		phase.NSPerOp = float64(elapsed.Nanoseconds()) / float64(phase.Operations)
		phase.OpsPerSec = float64(phase.Operations) / elapsed.Seconds()
	}
	if phase.LogicalBytes > 0 {
		phase.BytesPerSec = float64(phase.LogicalBytes) / elapsed.Seconds()
	}
	phase.Memory = benchmarkMemDelta{
		Mallocs:        after.Mallocs - before.Mallocs,
		TotalAlloc:     after.TotalAlloc - before.TotalAlloc,
		HeapAllocDelta: int64(after.HeapAlloc) - int64(before.HeapAlloc),
		NumGC:          after.NumGC - before.NumGC,
	}
	return phase, err
}
func populateBenchmark(store *db.KV, opts localBenchmarkOptions, phase *benchmarkPhase) (int64, error) {
	value := makeBenchmarkValue(opts.valueSize)
	var updated int64
	for start := 0; start < opts.keys; start += opts.batchSize {
		stop := start + opts.batchSize
		if stop > opts.keys {
			stop = opts.keys
		}
		tx := store.NewTX()
		for i := start; i < stop; i++ {
			ok, err := tx.Set(makeBenchmarkKey(i, opts.keySize), value)
			if err != nil {
				tx.Abort()
				phase.Errors++
				return updated, fmt.Errorf("set key %d: %w", i, err)
			}
			if ok {
				updated++
			}
		}
		if err := tx.Commit(); err != nil {
			phase.Errors++
			return updated, fmt.Errorf("commit keys %d-%d: %w", start, stop-1, err)
		}
		phase.Operations += int64(stop - start)
		phase.LogicalBytes += int64(stop-start) * int64(opts.keySize+opts.valueSize)
	}
	return updated, nil
}

func randomReadBenchmark(store *db.KV, opts localBenchmarkOptions, phase *benchmarkPhase) error {
	key := make([]byte, opts.keySize)
	state := benchmarkSeed
	for i := 0; i < opts.readOps; i++ {
		state = nextBenchmarkRand(state)
		idx := int(state % uint64(opts.keys))
		fillBenchmarkKey(key, idx)
		val, ok, err := store.Get(key)
		phase.Operations++
		phase.LogicalBytes += int64(opts.keySize)
		if err != nil {
			phase.Errors++
			return fmt.Errorf("get key %d: %w", idx, err)
		}
		if !ok {
			phase.Missing++
			continue
		}
		phase.Found++
		phase.LogicalBytes += int64(len(val))
		phase.Checksum += checksumBytes(val)
		if len(val) != opts.valueSize {
			phase.Errors++
			return fmt.Errorf("get key %d returned value size %d, want %d", idx, len(val), opts.valueSize)
		}
	}
	if phase.Missing != 0 {
		return fmt.Errorf("random_read missing %d keys", phase.Missing)
	}
	return nil
}

func warmBenchmarkCache(store *db.KV, opts localBenchmarkOptions) error {
	var phase benchmarkPhase
	if err := randomReadBenchmark(store, opts, &phase); err != nil {
		return fmt.Errorf("warm cache: %w", err)
	}
	return nil
}

func iterateBenchmark(store *db.KV, opts localBenchmarkOptions, values bool, phase *benchmarkPhase) error {
	iter, cleanup, err := store.IterAll()
	if err != nil {
		return fmt.Errorf("open iterator: %w", err)
	}
	defer cleanup()

	limit := expectedBenchmarkScanCount(opts)
	for iter.Valid() && int(phase.Operations) < limit {
		key := iter.Key()
		phase.LogicalBytes += int64(len(key))
		phase.Checksum += checksumBytes(key)
		if values {
			val := iter.Val()
			phase.LogicalBytes += int64(len(val))
			phase.Checksum += checksumBytes(val)
			if len(val) != opts.valueSize {
				phase.Errors++
				return fmt.Errorf("iterator value size %d, want %d", len(val), opts.valueSize)
			}
		}
		phase.Operations++
		if err := iter.Next(); err != nil {
			phase.Errors++
			return fmt.Errorf("advance iterator: %w", err)
		}
	}
	return nil
}

func expectedBenchmarkScanCount(opts localBenchmarkOptions) int {
	if opts.scanLimit > 0 && opts.scanLimit < opts.keys {
		return opts.scanLimit
	}
	return opts.keys
}

func makeBenchmarkValue(size int) []byte {
	value := make([]byte, size)
	for i := range value {
		value[i] = byte('a' + (i % 26))
	}
	return value
}

func makeBenchmarkKey(n, size int) []byte {
	key := make([]byte, size)
	fillBenchmarkKey(key, n)
	return key
}

func fillBenchmarkKey(dst []byte, n int) {
	for i := range dst {
		dst[i] = '0'
	}
	pos := len(dst)
	if n == 0 {
		dst[pos-1] = '0'
		return
	}
	for n > 0 {
		pos--
		dst[pos] = byte('0' + n%10)
		n /= 10
	}
}

func decimalDigits(n int) int {
	if n == 0 {
		return 1
	}
	digits := 0
	for n > 0 {
		digits++
		n /= 10
	}
	return digits
}

func nextBenchmarkRand(x uint64) uint64 {
	x ^= x << 7
	x ^= x >> 9
	x ^= x << 8
	return x
}

func checksumBytes(b []byte) uint64 {
	var sum uint64
	for _, c := range b {
		sum += uint64(c)
	}
	return sum
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func writeBenchmarkReport(w io.Writer, report benchmarkReport, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text":
		writeTextBenchmarkReport(w, report)
		return nil
	default:
		return fmt.Errorf("invalid format %q", format)
	}
}

func writeTextBenchmarkReport(w io.Writer, report benchmarkReport) {
	_, _ = fmt.Fprintln(w, "PallasDB local benchmark")
	_, _ = fmt.Fprintf(w, "started_at: %s\n", report.StartedAt)
	_, _ = fmt.Fprintf(w, "command: %s\n", report.Command)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "parameters:")
	_, _ = fmt.Fprintf(w, "  data_dir: %s\n", report.Parameters.DataDir)
	_, _ = fmt.Fprintf(w, "  keys: %d\n", report.Parameters.Keys)
	_, _ = fmt.Fprintf(w, "  value_size: %d\n", report.Parameters.ValueSize)
	_, _ = fmt.Fprintf(w, "  key_size: %d\n", report.Parameters.KeySize)
	_, _ = fmt.Fprintf(w, "  batch_size: %d\n", report.Parameters.BatchSize)
	_, _ = fmt.Fprintf(w, "  read_ops: %d\n", report.Parameters.ReadOps)
	_, _ = fmt.Fprintf(w, "  scan_limit: %d\n", report.Parameters.ScanLimit)
	_, _ = fmt.Fprintf(w, "  log_threshold: %d\n", report.Parameters.LogThreshold)
	_, _ = fmt.Fprintf(w, "  growth_factor: %.2f\n", report.Parameters.GrowthFactor)
	_, _ = fmt.Fprintf(w, "  auto_compact: %t\n", report.Parameters.AutoCompact)
	_, _ = fmt.Fprintf(w, "  compact: %t\n", report.Parameters.Compact)
	_, _ = fmt.Fprintf(w, "  cache_bytes: %d\n", report.Parameters.CacheBytes)
	_, _ = fmt.Fprintf(w, "  cache_counters: %d\n", report.Parameters.CacheCounters)
	_, _ = fmt.Fprintf(w, "  reset: %t\n", report.Parameters.Reset)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "environment:")
	_, _ = fmt.Fprintf(w, "  goos: %s\n", report.Environment.GOOS)
	_, _ = fmt.Fprintf(w, "  goarch: %s\n", report.Environment.GOARCH)
	_, _ = fmt.Fprintf(w, "  go_version: %s\n", report.Environment.GoVersion)
	_, _ = fmt.Fprintf(w, "  gomaxprocs: %d\n", report.Environment.GOMAXPROCS)
	_, _ = fmt.Fprintf(w, "  num_cpu: %d\n", report.Environment.NumCPU)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "phases:")
	for _, phase := range report.Phases {
		_, _ = fmt.Fprintf(w, "  %s:\n", phase.Name)
		_, _ = fmt.Fprintf(w, "    elapsed: %s\n", phase.Elapsed)
		_, _ = fmt.Fprintf(w, "    operations: %d\n", phase.Operations)
		_, _ = fmt.Fprintf(w, "    ns_per_op: %.2f\n", phase.NSPerOp)
		_, _ = fmt.Fprintf(w, "    ops_per_sec: %.2f\n", phase.OpsPerSec)
		_, _ = fmt.Fprintf(w, "    logical_bytes: %d\n", phase.LogicalBytes)
		_, _ = fmt.Fprintf(w, "    bytes_per_sec: %.2f\n", phase.BytesPerSec)
		_, _ = fmt.Fprintf(w, "    found: %d\n", phase.Found)
		_, _ = fmt.Fprintf(w, "    missing: %d\n", phase.Missing)
		_, _ = fmt.Fprintf(w, "    updated: %d\n", phase.Updated)
		_, _ = fmt.Fprintf(w, "    errors: %d\n", phase.Errors)
		_, _ = fmt.Fprintf(w, "    checksum: %d\n", phase.Checksum)
		_, _ = fmt.Fprintf(w, "    mallocs: %d\n", phase.Memory.Mallocs)
		_, _ = fmt.Fprintf(w, "    total_alloc_bytes: %d\n", phase.Memory.TotalAlloc)
		_, _ = fmt.Fprintf(w, "    heap_alloc_delta_bytes: %d\n", phase.Memory.HeapAllocDelta)
		_, _ = fmt.Fprintf(w, "    num_gc: %d\n", phase.Memory.NumGC)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "data_dir_size_bytes: %d\n", report.DataDirSizeBytes)
	_, _ = fmt.Fprintln(w, "---")
}

func appendBenchmarkOutput(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err == nil && info.Size() > 0 {
		if _, err := file.WriteString("\n"); err != nil {
			return fmt.Errorf("separate output: %w", err)
		}
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func benchmarkCommandLine() string {
	if cmd := os.Getenv("PALLASDB_BENCH_COMMAND"); cmd != "" {
		return cmd
	}
	parts := make([]string, len(os.Args))
	for i, arg := range os.Args {
		parts[i] = shellQuote(arg)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r != '-' && r != '_' && r != '.' && r != '/' && r != ':' && r != '=' && r != '+' && r != ',' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
