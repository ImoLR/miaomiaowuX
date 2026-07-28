package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"miaomiaowux/internal/auth"
)

func TestDashboardSystemPercentAndSnapshot(t *testing.T) {
	samples := []cpuTimes{
		{user: 150, system: 150, idle: 900},
	}
	h := &DashboardSystemHandler{
		readStat: func() (cpuTimes, error) {
			if len(samples) == 0 {
				return cpuTimes{}, errors.New("no samples")
			}
			out := samples[0]
			samples = samples[1:]
			return out, nil
		},
		readMem: func() (memoryStats, error) {
			return memoryStats{
				memTotal: 1024, memAvailable: 256, swapTotal: 512, swapFree: 128,
				hasMemTotal: true, hasMemAvail: true, hasSwapTotal: true, hasSwapFree: true,
			}, nil
		},
		readDisk: func(string) (diskStats, error) {
			return diskStats{used: 300, total: 1000}, nil
		},
	}
	h.lastCPU = cpuTimes{user: 100, system: 100, idle: 800}
	h.hasCPU = true

	got, err := h.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got.CPUPct != 50 {
		t.Fatalf("CPUPct = %v, want 50", got.CPUPct)
	}
	if got.MemUsed != 768 || got.MemTotal != 1024 {
		t.Fatalf("memory = %d/%d, want 768/1024", got.MemUsed, got.MemTotal)
	}
	if got.SwapUsed != 384 || got.SwapTotal != 512 {
		t.Fatalf("swap = %d/%d, want 384/512", got.SwapUsed, got.SwapTotal)
	}
	if got.DiskUsed != 300 || got.DiskTotal != 1000 {
		t.Fatalf("disk = %d/%d, want 300/1000", got.DiskUsed, got.DiskTotal)
	}
}

func TestParseMeminfoTotalZeroSafe(t *testing.T) {
	got := parseMeminfo("MemTotal: 0 kB\nMemAvailable: 0 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	if got.memTotal != 0 || got.memAvailable != 0 || got.swapTotal != 0 || got.swapFree != 0 {
		t.Fatalf("unexpected meminfo parse: %+v", got)
	}
	if clampMetricPercent(-1) != 0 || clampMetricPercent(101) != 100 {
		t.Fatalf("clampMetricPercent bounds failed")
	}
}

func TestDashboardSystemRequiresAdmin(t *testing.T) {
	store := auth.NewTokenStore(time.Hour)
	repo := fakeDashboardUserRepo{}
	handler := auth.RequireAdmin(store, repo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/dashboard/system", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	token, _, err := store.Issue("admin")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/dashboard/system", nil)
	req.Header.Set(auth.AuthHeader, token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want %d", rec.Code, http.StatusOK)
	}
}

type fakeDashboardUserRepo struct{}

func (fakeDashboardUserRepo) GetUser(context.Context, string) (auth.User, error) {
	return auth.User{Username: "admin", Role: auth.RoleAdmin, IsActive: true}, nil
}

func (fakeDashboardUserRepo) GetAPIToken(context.Context) (string, error) {
	return "", nil
}

func (fakeDashboardUserRepo) ResolveAPIToken(context.Context, string) (string, bool) {
	return "", false
}
