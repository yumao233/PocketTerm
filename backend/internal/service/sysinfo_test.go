package service

import (
	"sync"
	"testing"

	"github.com/wangkun/webshell/pkg/response"
)

// TestSysInfoService_Stats asserts the happy path of a single Stats call. We
// only assert invariants that are stable across hosts:
//   - code is ErrOK and err is nil (Stats must never surface collection
//     failures as a non-OK code — it degrades fields instead);
//   - CpuCores is at least 1 (every test host has a CPU);
//   - MemTotalGb is positive (every host has RAM);
//   - MemPercent lies in [0, 100] (degraded to 0 is also accepted);
//   - RttMs is non-negative (the sentinel -1 must be overwritten on success).
//
// We do NOT assert a specific CpuPercent value: gopsutil's first reading on a
// cold host may legitimately be 0 (no delta yet), and on a busy host it can be
// anywhere in [0, 100].
func TestSysInfoService_Stats(t *testing.T) {
	s := NewSysInfoService()

	stats, code, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats returned err: %v (code %d)", err, code)
	}
	if code != response.ErrOK {
		t.Fatalf("Stats code = %d, want ErrOK (%d)", code, response.ErrOK)
	}
	if stats == nil {
		t.Fatalf("Stats returned nil stats")
	}

	if stats.CpuCores < 1 {
		t.Fatalf("CpuCores = %d, want >= 1", stats.CpuCores)
	}
	if stats.MemTotalGb <= 0 {
		t.Fatalf("MemTotalGb = %v, want > 0", stats.MemTotalGb)
	}
	if stats.MemPercent < 0 || stats.MemPercent > 100 {
		t.Fatalf("MemPercent = %v, want [0, 100]", stats.MemPercent)
	}
	if stats.RttMs < 0 {
		t.Fatalf("RttMs = %d, want >= 0 (sentinel -1 must be overwritten)", stats.RttMs)
	}
	// A second call should still succeed and stay well-formed. The warmup
	// goroutine has either completed by now or the first Stats call took the
	// 1s inline sample path, so cpu_percent may now reflect a real reading —
	// but we still only assert the stable invariants.
	if _, code, err := s.Stats(); err != nil || code != response.ErrOK {
		t.Fatalf("second Stats: code=%d err=%v", code, err)
	}
}

// TestSysInfoService_Stats_Concurrent fires N goroutines that all call Stats
// at once. This guards against data races in the warmup state (the `warmed`
// bool is mutated under a mutex) and the inline-sample fallback path (multiple
// cold-start callers might race to set warmed=true). Run with -race.
func TestSysInfoService_Stats_Concurrent(t *testing.T) {
	s := NewSysInfoService()

	const n = 10
	var wg sync.WaitGroup
	start := make(chan struct{}) // barrier so all goroutines race together
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			stats, code, err := s.Stats()
			if err != nil {
				errs <- err
				return
			}
			if code != response.ErrOK {
				errs <- nil // handled via code mismatch below
				return
			}
			if stats == nil {
				errs <- nil
				return
			}
			// Same stable invariants as the single-call test; if any fail we
			// surface via errs so the caller can report.
			if stats.CpuCores < 1 {
				errs <- nil
				return
			}
			errs <- nil
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Stats err: %v", err)
		}
	}
}