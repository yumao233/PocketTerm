package service

import (
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"

	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/pkg/response"
)

// sysinfoWarmupInterval is the sleep used during the one-shot warmup call to
// cpu.Percent. gopsutil computes CPU utilization by comparing two samples of
// /proc/stat (or the platform equivalent); the first call therefore needs a
// non-zero interval to establish a baseline. We do this once at construction
// time so Stats() can subsequently pass interval=0 and return immediately.
const sysinfoWarmupInterval = time.Second

// SysInfoService reports real-time host resource usage (CPU/RAM) plus a coarse
// process-level round-trip time approximation. It wraps gopsutil; any failure
// in the underlying collection degrades the corresponding field to 0 (or -1
// for RTT) rather than producing a 5xx — the monitoring strip is best-effort.
type SysInfoService struct {
	// mu guards the warmup state. After warmup completes, Stats() hot path
	// takes this lock only to flip `warmed` to true on the very first call;
	// subsequent calls skip the lock-protected branch entirely via an atomic
	// load of the warmed flag. We keep a mutex (rather than sync.Once) so a
	// warmup failure can be retried lazily by the next Stats() call.
	mu      sync.Mutex
	warmed  bool
	warmErr error
}

// NewSysInfoService builds a SysInfoService and kicks off a background warmup
// of gopsutil's CPU sampler. The warmup runs in its own goroutine and does not
// block construction; the first Stats() call waits for it (or falls back to a
// fresh sample) so callers never observe a zero CPU reading forever.
func NewSysInfoService() *SysInfoService {
	s := &SysInfoService{}
	go s.warmup()
	return s
}

// warmup primes gopsutil's internal CPU baseline so the first user-visible
// Stats() call returns a real percentage instead of 0. cpu.Percent with a
// non-zero interval blocks for that duration while sampling; running it here
// in the background hides that latency from the first request. Any error is
// recorded but non-fatal — Stats() will simply retry on demand.
func (s *SysInfoService) warmup() {
	// cpu.Percent blocks for sysinfoWarmupInterval to measure the delta. We
	// ignore the returned value: the call's side-effect (gopsutil now has a
	// baseline) is what we actually want.
	if _, err := cpu.Percent(sysinfoWarmupInterval, false); err != nil {
		s.mu.Lock()
		s.warmErr = err
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.warmed = true
	s.mu.Unlock()
}

// Stats returns a host-wide resource snapshot. It NEVER returns a non-OK code:
// any per-metric collection failure degrades that single field to 0 (or -1 for
// RTT) while the rest of the snapshot is still populated. This matches the
// monitoring strip's "best-effort" semantics — a transient /proc read failure
// must not break the whole dashboard.
//
// CPU: if warmup hasn't completed yet (cold start within the first second of
// process life), this call takes a 1s sample inline so the caller still gets a
// real number rather than 0; once warmed, the call is non-blocking.
func (s *SysInfoService) Stats() (*model.SystemStats, int, error) {
	stats := &model.SystemStats{
		RttMs: -1, // sentinel: "not measured"; overwritten below on success
	}

	// --- RTT (process-local monotonic round-trip approximation) ---
	// This is intentionally NOT a network ping: it measures the minimum
	// observable round-trip of a single time.Now() → time.Since() pair in this
	// process. On modern hardware this is sub-millisecond, so the integer ms
	// truncation frequently yields 0 — that is expected and useful as a "we
	// are alive and the clock works" trend signal, not an absolute latency.
	rttStart := time.Now()
	rtt := time.Since(rttStart)
	stats.RttMs = rtt.Milliseconds()

	// --- CPU ---
	// Decide whether we can use the fast (interval=0) path. We re-check
	// warmup each call (cheap mutex + bool) so a slow warmup goroutine is not
	// a permanent block: once it flips `warmed`, all subsequent calls skip the
	// 1s inline sample.
	s.mu.Lock()
	warmed := s.warmed
	s.mu.Unlock()
	if warmed {
		// Interval=0 returns the delta since the last sample (the warmup
		// call). Best-effort: on failure leave CpuPercent=0 and try to count
		// cores anyway.
		if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
			stats.CpuPercent = pct[0]
		}
	} else {
		// Warmup hasn't completed (cold start). Take an inline 1s sample so
		// the caller still gets a real number. The first such inline call
		// also serves as warmup for later calls.
		if pct, err := cpu.Percent(sysinfoWarmupInterval, false); err == nil && len(pct) > 0 {
			stats.CpuPercent = pct[0]
			// Side-effect: mark warmed so subsequent calls skip this branch.
			// Best-effort — if another goroutine already set it, no harm.
			s.mu.Lock()
			s.warmed = true
			s.warmErr = nil
			s.mu.Unlock()
		}
	}
	// Logical core count is independent of the sampler baseline; safe to call
	// without warmup.
	if cores, err := cpu.Counts(true); err == nil && cores > 0 {
		stats.CpuCores = cores
	}

	// --- Memory ---
	// VirtualMemory is a single syscall (/proc/meminfo on Linux, sysctl on
	// macOS); no baseline needed.
	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		const bytesPerGB = 1024 * 1024 * 1024
		stats.MemTotalGb = float64(vm.Total) / bytesPerGB
		stats.MemUsedGb = float64(vm.Used) / bytesPerGB
		stats.MemPercent = vm.UsedPercent
	}

	return stats, response.ErrOK, nil
}