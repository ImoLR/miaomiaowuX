package handler

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"miaomiaowux/internal/auth"
	"miaomiaowux/internal/logger"
	"miaomiaowux/internal/storage"
)

var globalSilentModeManager *SilentModeManager

type SilentModeManager struct {
	repo                 *storage.TrafficRepository
	tokens               *auth.TokenStore
	lastActiveTime       sync.Map
	lastGlobalActiveTime time.Time
	globalActiveMu       sync.Mutex
	startTime            time.Time
	shortLinkSet         map[string]struct{}
	shortLinkSetMu       sync.RWMutex
	shortLinkSetTime     time.Time
}

func NewSilentModeManager(repo *storage.TrafficRepository, tokens *auth.TokenStore) *SilentModeManager {
	m := &SilentModeManager{
		repo:      repo,
		tokens:    tokens,
		startTime: time.Now(),
	}
	globalSilentModeManager = m
	logger.Info("🔓 [SILENT_MODE] 服务启动，静默模式临时恢复中",
		"start_time", m.startTime.Format("2006-01-02 15:04:05"),
	)
	return m
}

func GetSilentModeManager() *SilentModeManager {
	return globalSilentModeManager
}

func (m *SilentModeManager) InvalidateShortLinkCache() {
	m.shortLinkSetMu.Lock()
	m.shortLinkSetTime = time.Time{}
	m.shortLinkSetMu.Unlock()
}

func (m *SilentModeManager) refreshShortLinkSet() {
	ctx := context.Background()
	fileCodes, err := m.repo.GetAllFileShortCodes(ctx)
	if err != nil {
		return
	}
	userCodes, err := m.repo.GetAllUserShortCodes(ctx)
	if err != nil {
		return
	}
	// package 短码查失败不致命(多数部署没套餐),忽略错误按空处理。
	packageCodes, _ := m.repo.GetAllPackageShortCodes(ctx)

	set := make(map[string]struct{})
	// 新版单码 /x/{code}:file_short_code / custom_short_code / package.short_code 各自单独
	// 命中一条订阅(与 short_link.go TryServe 的 GetSubscribeFileByShortCode 对齐)。
	// 之前只塞下面的复合码,新版单码永远进不了 set,是 /x/ 被静默模式误杀的原因之一。
	for fc := range fileCodes {
		set[fc] = struct{}{}
	}
	for pc := range packageCodes {
		set[pc] = struct{}{}
	}
	// 旧版复合码 /x/{fileCode|packageCode}{userCode}(TryServe 的 fallback 拆分匹配)。
	for uc := range userCodes {
		for fc := range fileCodes {
			set[fc+uc] = struct{}{}
		}
		for pc := range packageCodes {
			set[pc+uc] = struct{}{}
		}
	}

	m.shortLinkSetMu.Lock()
	m.shortLinkSet = set
	m.shortLinkSetTime = time.Now()
	m.shortLinkSetMu.Unlock()
}

func (m *SilentModeManager) isKnownShortLink(path string) bool {
	if len(path) < 2 || !isAlphanumericPath(path) {
		return false
	}

	m.shortLinkSetMu.RLock()
	expired := time.Since(m.shortLinkSetTime) > 60*time.Second
	m.shortLinkSetMu.RUnlock()

	if expired {
		m.refreshShortLinkSet()
	}

	m.shortLinkSetMu.RLock()
	_, ok := m.shortLinkSet[path]
	m.shortLinkSetMu.RUnlock()
	return ok
}

func (m *SilentModeManager) RecordSubscriptionAccessWithIP(username, ip string) {
	if username == "" {
		return
	}
	now := time.Now()
	m.lastActiveTime.Store(username, now)

	m.globalActiveMu.Lock()
	m.lastGlobalActiveTime = now
	m.globalActiveMu.Unlock()

	// 不手动传 "time" —— slog 已自动加 time= 字段,重复会让一行出现两个 time=,污染日志解析。
	logger.Info("🔓 [SILENT_MODE] 用户获取订阅，恢复所有IP访问权限",
		"username", username,
		"ip", ip,
	)
}

func (m *SilentModeManager) isUserActive(username string, timeout int) bool {
	if username == "" {
		return false
	}

	val, ok := m.lastActiveTime.Load(username)
	if !ok {
		return false
	}

	lastActive := val.(time.Time)
	activeUntil := lastActive.Add(time.Duration(timeout) * time.Minute)
	return time.Now().Before(activeUntil)
}

func (m *SilentModeManager) isGlobalActive(timeout int) bool {
	m.globalActiveMu.Lock()
	lastActive := m.lastGlobalActiveTime
	m.globalActiveMu.Unlock()

	if lastActive.IsZero() {
		return false
	}

	activeUntil := lastActive.Add(time.Duration(timeout) * time.Minute)
	return time.Now().Before(activeUntil)
}

func (m *SilentModeManager) extractUsername(r *http.Request) string {
	if m.tokens == nil {
		return ""
	}

	if token := strings.TrimSpace(r.Header.Get(auth.AuthHeader)); token != "" {
		if username, ok := m.tokens.Lookup(token); ok {
			return username
		}
	}

	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		if username, ok := m.tokens.Lookup(token); ok {
			return username
		}
	}

	return ""
}

func (m *SilentModeManager) isAllowedPath(path string) bool {
	allowedPrefixes := []string{
		"/api/clash/subscribe",
		"/api/proxy-provider/",
		"/t/",
	}

	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	trimmedPath := strings.Trim(path, "/")
	// /x/{code} 短链接:必须先剥掉 "x/" 前缀再判断。原来直接把 "x/{code}" 交给
	// isKnownShortLink,其中的 '/' 会让 isAlphanumericPath 立刻返回 false,导致所有 /x/
	// 短链接被静默模式误杀 —— 下不了订阅,也就无从触发 RecordSubscriptionAccessWithIP 解锁主控。
	if code, ok := strings.CutPrefix(trimmedPath, "x/"); ok {
		if m.isKnownShortLink(code) {
			return true
		}
	}

	return false
}

func isAlphanumericPath(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func (m *SilentModeManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, err := m.repo.GetSystemConfig(context.Background())
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		if !cfg.SilentMode {
			next.ServeHTTP(w, r)
			return
		}

		recoveryUntil := m.startTime.Add(time.Duration(cfg.SilentModeTimeout) * time.Minute)
		if time.Now().Before(recoveryUntil) {
			next.ServeHTTP(w, r)
			return
		}

		if m.isAllowedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		username := m.extractUsername(r)
		clientIP := GetClientIP(r)

		if username != "" && m.isUserActive(username, cfg.SilentModeTimeout) {
			next.ServeHTTP(w, r)
			return
		}

		if m.isGlobalActive(cfg.SilentModeTimeout) {
			next.ServeHTTP(w, r)
			return
		}

		logger.Info("🔒 [SILENT_MODE] 请求被拦截",
			"path", r.URL.Path,
			"username", username,
			"client_ip", clientIP,
		)
		w.Header().Set("X-Silent-Mode", "true")
		http.NotFound(w, r)
	})
}
