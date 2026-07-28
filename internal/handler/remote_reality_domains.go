package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"miaomiaowux/internal/storage"
	"miaomiaowux/templates"
)

type realityDomainLatencyProbeRequest struct {
	Domains   []string `json:"domains"`
	TimeoutMs int      `json:"timeout_ms,omitempty"`
}

type realityDomainLatencyProbeResult struct {
	Domain       string `json:"domain"`
	Target       string `json:"target"`
	Success      bool   `json:"success"`
	LatencyMs    int64  `json:"latency_ms,omitempty"`
	Error        string `json:"error,omitempty"`
	NginxSSLPort int    `json:"nginx_ssl_port,omitempty"`
}

type realityDomainLatencyProbeResponse struct {
	Success bool                              `json:"success"`
	Results []realityDomainLatencyProbeResult `json:"results"`
	Message string                            `json:"message,omitempty"`
	Error   string                            `json:"error,omitempty"`
}

type domainServerInfo struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	Domain     string `json:"domain"`
}

// 返回由所选远程服务器探测的域延迟结果（低 -> 高）。
func (h *RemoteManageHandler) HandleRealityDomains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	serverIDStr := r.URL.Query().Get("server_id")
	if serverIDStr == "" {
		remoteWriteError(w, http.StatusBadRequest, "server_id required")
		return
	}
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil || serverID <= 0 {
		remoteWriteError(w, http.StatusBadRequest, "invalid server_id")
		return
	}

	timeoutMs := 2000
	if timeoutStr := r.URL.Query().Get("timeout_ms"); timeoutStr != "" {
		if parsed, parseErr := strconv.Atoi(timeoutStr); parseErr == nil {
			if parsed < 200 {
				parsed = 200
			}
			if parsed > 10000 {
				parsed = 10000
			}
			timeoutMs = parsed
		}
	}

	inventory, err := h.collectRealityDomainInventory(r.Context())
	if err != nil {
		remoteWriteError(w, http.StatusInternalServerError, fmt.Sprintf("failed to collect domain candidates: %v", err))
		return
	}
	candidates := inventory.Domains
	domainServerMap := inventory.ServerMap

	if len(candidates) == 0 {
		remoteWriteJSON(w, http.StatusOK, map[string]any{
			"success":          true,
			"message":          "未在服务器配置中找到可用域名",
			"probe_server_id":  serverID,
			"total_candidates": 0,
			"domains":          []realityDomainLatencyProbeResult{},
			"domain_sources":   inventory.Sources,
			"blocked_domains":  inventory.Blocked,
		})
		return
	}

	// 通过 WebSocket 进行探测（代理在远程服务器上本地运行探测）
	var probeResults []realityDomainLatencyProbeResult

	if h.wsHandler != nil {
		wsResult, err := h.wsHandler.SendDomainLatencyProbe(serverID, candidates, timeoutMs)
		if err != nil {
			log.Printf("[Remote Manage] WebSocket probe failed for server %d, falling back to HTTP: %v", serverID, err)
		} else if wsResult != nil && wsResult.Success {
			for _, r := range wsResult.Results {
				probeResults = append(probeResults, realityDomainLatencyProbeResult{
					Domain:       r.Domain,
					Target:       r.Target,
					Success:      r.Success,
					LatencyMs:    r.LatencyMs,
					Error:        r.Error,
					NginxSSLPort: int(r.NginxSSLPort),
				})
			}
		}
	}

	// 如果 WebSocket 探测未产生结果，则回退到 HTTP 转发
	if len(probeResults) == 0 {
		reqPayload := realityDomainLatencyProbeRequest{
			Domains:   candidates,
			TimeoutMs: timeoutMs,
		}
		body, err := json.Marshal(reqPayload)
		if err != nil {
			remoteWriteError(w, http.StatusInternalServerError, "failed to build probe request")
			return
		}

		result, err := h.forwardToRemoteServer(r.Context(), serverID, http.MethodPost, "/api/child/domains/latency", body)
		if err != nil {
			failedResults := make([]realityDomainLatencyProbeResult, 0, len(candidates))
			for _, d := range candidates {
				failedResults = append(failedResults, realityDomainLatencyProbeResult{
					Domain:  d,
					Target:  d + ":443",
					Success: false,
					Error:   err.Error(),
				})
			}
			remoteWriteJSON(w, http.StatusOK, map[string]any{
				"success":          true,
				"probe_server_id":  serverID,
				"total_candidates": len(candidates),
				"domains":          failedResults,
				"domain_servers":   domainServerMap,
				"domain_sources":   inventory.Sources,
				"blocked_domains":  inventory.Blocked,
				"warning":          fmt.Sprintf("探测失败: %v", err),
			})
			return
		}

		var probeResp realityDomainLatencyProbeResponse
		if err := json.Unmarshal(result, &probeResp); err != nil {
			remoteWriteError(w, http.StatusInternalServerError, fmt.Sprintf("failed to parse probe response: %v", err))
			return
		}

		if !probeResp.Success {
			if probeResp.Error != "" {
				remoteWriteError(w, http.StatusBadGateway, probeResp.Error)
				return
			}
			remoteWriteError(w, http.StatusBadGateway, "domain probe failed")
			return
		}

		probeResults = probeResp.Results
	}

	remoteWriteJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"probe_server_id":  serverID,
		"total_candidates": len(candidates),
		"domains":          probeResults,
		"domain_servers":   domainServerMap,
		"domain_sources":   inventory.Sources,
		"blocked_domains":  inventory.Blocked,
	})
}

// collectRealityDomainCandidates 是 collectRealityDomainInventory 的兼容包装,
// 保留原有三返回值签名供既有调用方使用。
func (h *RemoteManageHandler) collectRealityDomainCandidates(ctx context.Context) ([]string, map[string]domainServerInfo, error) {
	inv, err := h.collectRealityDomainInventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	return inv.Domains, inv.ServerMap, nil
}

// collectRealityDomainInventory 收集全部候选域名,并记录每个域名的来源。
//
// 来源信息有两个用途:①前端展示,让用户知道某个域名是哪来的;②共享功能据此区分
// 「客户自有域名」和「公共偷取目标」,只有后者才允许上报(见 reality_domain_inventory.go)。
//
// 返回的 Domains 已剔除屏蔽名单——屏蔽在**出口统一做**,这样不论域名来自主控 URL、
// 服务器配置还是 agent 实配,删掉之后都不会在下次刷新时复活。
func (h *RemoteManageHandler) collectRealityDomainInventory(ctx context.Context) (*realityDomainInventory, error) {
	servers, err := h.repo.ListRemoteServers(ctx)
	if err != nil {
		return nil, err
	}

	acc := newDomainAccumulator()
	domainServerMap := make(map[string]domainServerInfo)

	if masterDomain := getDomainFromMasterURL(h.repo, ctx); masterDomain != "" {
		acc.add(masterDomain, domainSourceMaster)
	}

	for _, raw := range loadDomainListSetting(ctx, h.repo, realityDomainsSettingKey) {
		acc.add(raw, domainSourceCustom)
	}

	for _, server := range servers {
		serverDomainSources := []string{server.Domain, server.PullAddress}
		for _, source := range serverDomainSources {
			if source == "" {
				continue
			}
			for _, raw := range strings.Split(source, ",") {
				d := acc.add(raw, domainSourceServer)
				if d == "" {
					continue
				}
				domainServerMap[d] = domainServerInfo{
					ServerID:   server.ID,
					ServerName: server.Name,
					Domain:     d,
				}
			}
		}

		// steal-self 服务器偷的是自己的域名,它的 dest 必须整台排除,不能当公共站共享出去
		stealSelf := isStealSelfServer(server)

		inbounds := h.fetchInboundsForDomainScan(ctx, server)
		for _, inbound := range inbounds {
			extractDomainsFromInbound(inbound, acc, stealSelf)
		}
	}

	// 共享池:开启共享的用户可以拿到别人贡献并经服务端验证过的域名。
	// 标 shared_pool 来源,selectShareableDomains 会据此排除它们再上报,
	// 避免池内域名在用户之间来回传导致贡献计数虚高。
	if h.shareEnabled(ctx) && h.realityPoolLicensed() {
		if pool, poolErr := h.licenseManager.ListRealityDomains(ctx); poolErr == nil {
			for _, p := range pool {
				acc.add(p.Domain, domainSourceSharedPool)
			}
		} else {
			log.Printf("[reality-share] 拉取共享池失败(不影响本地候选): %v", poolErr)
		}
	}

	blocked := blockedDomainSet(ctx, h.repo)
	out := make([]string, 0, len(acc.order))
	for _, d := range acc.order {
		if _, isBlocked := blocked[d]; isBlocked {
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)

	blockedList := make([]string, 0, len(blocked))
	for d := range blocked {
		blockedList = append(blockedList, d)
	}
	sort.Strings(blockedList)

	return &realityDomainInventory{
		Domains:     out,
		Sources:     acc.sources,
		ServerMap:   domainServerMap,
		SelfOwned:   acc.selfOwned,
		RealityDest: acc.realityDest,
		Blocked:     blockedList,
	}, nil
}

// fetchInboundsForDomainScan 取一台服务器上的入站配置,供域名扫描使用。
//
// 在线时问 agent 要实配;离线/失败时回落到 server_xray_config_snapshots 里的最后一份快照。
// 有回落才能扫全:否则一台服务器只要当下不在线,它上面所有 reality 偷取目标就整批消失,
// 表现为共享清单里"有些域名看不见"。
func (h *RemoteManageHandler) fetchInboundsForDomainScan(ctx context.Context, server storage.RemoteServer) []map[string]interface{} {
	if server.Status == "connected" {
		result, err := h.forwardToRemoteServer(ctx, server.ID, http.MethodGet, "/api/child/inbounds", nil)
		if err == nil {
			var resp struct {
				Success  bool                     `json:"success"`
				Inbounds []map[string]interface{} `json:"inbounds"`
			}
			if jsonErr := json.Unmarshal(result, &resp); jsonErr == nil && resp.Success {
				return resp.Inbounds
			}
			log.Printf("[Remote Manage] Invalid inbounds response from server %d (%s), falling back to snapshot", server.ID, server.Name)
		} else {
			log.Printf("[Remote Manage] Live inbounds fetch failed for server %d (%s), falling back to snapshot: %v", server.ID, server.Name, err)
		}
	}

	snap, err := h.repo.GetCurrentXraySnapshot(ctx, server.ID)
	if err != nil || snap == nil || strings.TrimSpace(snap.ConfigJSON) == "" {
		return nil
	}
	var cfg struct {
		Inbounds []map[string]interface{} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(snap.ConfigJSON), &cfg); err != nil {
		log.Printf("[Remote Manage] Parse snapshot for server %d (%s) failed: %v", server.ID, server.Name, err)
		return nil
	}
	return cfg.Inbounds
}

// 配置 nginx SSL (443) + 在远程服务器上部署证书。
func (h *RemoteManageHandler) HandleSetupSSL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	id, err := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	if err != nil || id <= 0 {
		remoteWriteError(w, http.StatusBadRequest, "invalid server_id")
		return
	}

	server, err := h.repo.GetRemoteServer(r.Context(), id)
	if err != nil {
		remoteWriteError(w, http.StatusNotFound, "server not found")
		return
	}
	if server.Domain == "" {
		remoteWriteError(w, http.StatusBadRequest, "服务器未配置域名")
		return
	}

	domain := strings.ToLower(strings.TrimSpace(server.Domain))

	// 提取通配符证书的根域（例如 us1.example.com -> example.com）
	rootDomain := extractRootDomain(domain)

	// 步骤1：读取nginx.conf基本模板（无需域替换）
	nginxTplPath := "tunnel/nginx.conf"
	if server.StealMode == "fallback" {
		nginxTplPath = "fallback/nginx.conf"
	}
	nginxConf, readErr := templates.ReadFile(nginxTplPath)
	if readErr != nil {
		remoteWriteError(w, http.StatusInternalServerError, fmt.Sprintf("读取 nginx.conf 模板失败: %v", readErr))
		return
	}

	certName := "_." + rootDomain
	if h.certHandler != nil {
		if cert, certErr := h.repo.FindDeployableCertByDomain(r.Context(), rootDomain, id); certErr == nil && cert != nil {
			certName = certDeployFilename(cert.Domain)
		}
	}
	// 第2步：统一渲染 domain conf(伪装站 location / + 该 server 现有 ws location,reality偷自己+WSS 共存)
	domainConf, derr := renderStealSelfDomainConf(server.StealMode, server.SiteType, server.SiteValue, domain, certName, h.fetchWSSInbounds(r.Context(), id))
	if derr != nil {
		remoteWriteError(w, http.StatusInternalServerError, fmt.Sprintf("渲染 domain.conf 失败: %v", derr))
		return
	}

	sslPayload, _ := json.Marshal(map[string]any{
		"domain":        domain,
		"nginx_config":  string(nginxConf),
		"domain_config": domainConf,
	})
	_, err = h.forwardNginxSetupSSL(r.Context(), id, sslPayload)
	if err != nil {
		remoteWriteError(w, http.StatusBadGateway, fmt.Sprintf("配置 Nginx SSL 失败: %v", err))
		return
	}

	// 步骤 3：使用根域查找并部署通配符证书
	certDeployed := false
	if h.certHandler != nil {
		cert, certErr := h.repo.FindDeployableCertByDomain(r.Context(), rootDomain, id)
		if certErr == nil && cert != nil && cert.CertPEM != "" && cert.KeyPEM != "" {
			certPath := fmt.Sprintf("/usr/local/nginx/cert/%s.pem", certDeployFilename(cert.Domain))
			keyPath := fmt.Sprintf("/usr/local/nginx/cert/%s.key", certDeployFilename(cert.Domain))

			payload := WSCertDeployPayload{
				Domain:   rootDomain,
				CertPEM:  cert.CertPEM,
				KeyPEM:   cert.KeyPEM,
				CertPath: certPath,
				KeyPath:  keyPath,
				Reload:   "nginx",
			}
			h.certHandler.deployToRemoteServer(server, payload)
			certDeployed = true
		}

		if !certDeployed {
			// 尝试自动部署证书
			h.certHandler.DeployAutoDeployCertificates(id)
			certDeployed = true
		}
	}

	remoteWriteJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"message":       fmt.Sprintf("已为 %s 配置 SSL", domain),
		"cert_deployed": certDeployed,
	})
}

// 将 nginx.conf + domain.conf + config.json 部署到远程服务器。
func (h *RemoteManageHandler) HandleDeployStealSelfConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	id, err := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	if err != nil || id <= 0 {
		remoteWriteError(w, http.StatusBadRequest, "invalid server_id")
		return
	}

	// 使用独立 context：steal-self 部署会清理 nginx 443 端口，可能导致反代连接中断、请求 context 被取消
	deployCtx, deployCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer deployCancel()
	if err := h.DeployStealSelfConfig(deployCtx, id); err != nil {
		remoteWriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	remoteWriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "配置下发成功",
	})
}

// DeployStealSelfConfig 将配置部署到远程服务器，根据 steal_mode 选择对应配置:
//   - "fallback":需要 domain,下发 fallback 模板
//   - "tunnel":需要 domain,下发 tunnel 模板
//   - "default" / 空值:下发主控内嵌的 default/config.json 模板,无需 domain
//
// 历史 BUG:之前 if/else 只识别 fallback,其它(含 default、空)统统走 tunnel,
// 用户选了"默认"部署模式但 deployStealSelf 实际下发的是 tunnel 配置。
func (h *RemoteManageHandler) DeployStealSelfConfig(ctx context.Context, serverID int64) error {
	server, err := h.repo.GetRemoteServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("获取服务器信息失败: %w", err)
	}

	switch server.StealMode {
	case "fallback":
		if server.Domain == "" {
			return fmt.Errorf("fallback 模式需要先配置域名")
		}
		return h.deployFallbackConfig(ctx, server)
	case "tunnel":
		if server.Domain == "" {
			return fmt.Errorf("tunnel 模式需要先配置域名")
		}
		return h.deployTunnelConfig(ctx, server)
	default:
		// default / 空值都走主控内嵌默认模板。用户主动触发不跳过 has-config 检查
		// (跳过是为"全新装机自动下发"防覆盖业务,用户手动点这里说明就是要覆盖)。
		return h.deployDefaultConfigManual(ctx, serverID)
	}
}

// deployDefaultConfigManual 是 deployDefaultConfig 的"用户主动模式":不做 has-config 检查,
// 直接覆盖 agent 当前 xray 配置为内嵌默认模板,然后重启 xray。
func (h *RemoteManageHandler) deployDefaultConfigManual(ctx context.Context, serverID int64) error {
	configTpl, err := templates.ReadFile("default/config.json")
	if err != nil {
		return fmt.Errorf("读取默认配置模板: %w", err)
	}
	configPayload, _ := json.Marshal(map[string]string{"config": string(configTpl)})
	if _, err := h.forwardToRemoteServer(ctx, serverID, http.MethodPost, "/api/child/xray/config", configPayload); err != nil {
		return fmt.Errorf("下发默认配置: %w", err)
	}
	if err := h.restartXrayWithRecovery(ctx, serverID, "ManualDeployDefault"); err != nil {
		return fmt.Errorf("重启 xray: %w", err)
	}
	return nil
}

// extractDomainsFromInbound 从一个入站配置里抽取域名候选。
//
// 关键区分:realitySettings 里的 dest/serverNames 是**偷取目标**(可能是公共站),
// 而 tlsSettings.serverName 是这个入站自己的**证书域名**(必定是客户自有)。两者来源
// 必须分开标记,否则共享功能会把客户域名当公共站上报出去。
//
// stealSelf=true 时该服务器偷的是自己的域名,其 dest/serverNames 一并标为自有。
func extractDomainsFromInbound(inbound map[string]interface{}, acc *domainAccumulator, stealSelf bool) {
	streamSettings, _ := inbound["streamSettings"].(map[string]interface{})
	if streamSettings == nil {
		return
	}

	addReality := func(raw string) {
		d := acc.add(raw, domainSourceRealityDest)
		if d != "" && stealSelf {
			acc.markSelfOwned(d)
		}
	}

	if realitySettings, _ := streamSettings["realitySettings"].(map[string]interface{}); realitySettings != nil {
		if dest, ok := realitySettings["dest"].(string); ok {
			addReality(dest)
		}

		switch v := realitySettings["serverNames"].(type) {
		case []interface{}:
			for _, item := range v {
				if name, ok := item.(string); ok {
					addReality(name)
				}
			}
		case string:
			for _, item := range strings.Split(v, ",") {
				addReality(item)
			}
		}
	}

	if tlsSettings, _ := streamSettings["tlsSettings"].(map[string]interface{}); tlsSettings != nil {
		if serverName, ok := tlsSettings["serverName"].(string); ok {
			acc.add(serverName, domainSourceTLSSNI)
		}
	}
}

func normalizeDomainCandidate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	if strings.Contains(s, "://") {
		if idx := strings.Index(s, "://"); idx >= 0 && idx+3 < len(s) {
			s = s[idx+3:]
		}
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}

	s = strings.TrimSpace(strings.Trim(s, "[]"))
	if s == "" {
		return ""
	}

	if host, port, err := net.SplitHostPort(s); err == nil {
		if host != "" && port != "" {
			s = host
		}
	} else {
		if idx := strings.LastIndex(s, ":"); idx > 0 && idx < len(s)-1 {
			if _, err := strconv.Atoi(s[idx+1:]); err == nil {
				s = s[:idx]
			}
		}
	}

	s = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(s), "*."))
	if s == "" {
		return ""
	}

	// 仅保留域名；跳过纯 IP 候选者。
	if net.ParseIP(s) != nil {
		return ""
	}
	return s
}

// extractRootDomain 从子域返回根域。
// 例如“us1.example.com”->“example.com”，“example.com”->“example.com”
func extractRootDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return domain
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func (h *RemoteManageHandler) HandleAddCustomRealityDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Domain   string `json:"domain"`
		ServerID int64  `json:"server_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		remoteWriteError(w, http.StatusBadRequest, "invalid request")
		return
	}

	domain := normalizeDomainCandidate(req.Domain)
	if domain == "" {
		remoteWriteError(w, http.StatusBadRequest, "域名不能为空")
		return
	}

	ctx := r.Context()

	existing := loadDomainListSetting(ctx, h.repo, realityDomainsSettingKey)
	found := false
	for _, d := range existing {
		if d == domain {
			found = true
			break
		}
	}
	if !found {
		_ = saveDomainListSetting(ctx, h.repo, realityDomainsSettingKey, append(existing, domain))
	}

	// 手工添加视为「撤销屏蔽」:否则用户删过这个域名后再加回来会被屏蔽名单拦住,
	// 表现为"加了但列表里没有",无从排查。
	h.unblockRealityDomain(ctx, domain)

	result := map[string]any{
		"success":    true,
		"domain":     domain,
		"latency_ms": nil,
		"saved":      !found,
	}

	if req.ServerID > 0 && h.wsHandler != nil {
		wsResult, err := h.wsHandler.SendDomainLatencyProbe(req.ServerID, []string{domain}, 2000)
		if err == nil && wsResult != nil && wsResult.Success && len(wsResult.Results) > 0 {
			r := wsResult.Results[0]
			result["success"] = r.Success
			result["latency_ms"] = r.LatencyMs
			result["target"] = r.Target
			result["error"] = r.Error
			result["nginx_ssl_port"] = r.NginxSSLPort
		} else if err != nil {
			result["error"] = err.Error()
		}
	}

	remoteWriteJSON(w, http.StatusOK, result)
}

func (h *RemoteManageHandler) HandleDeleteCustomRealityDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		remoteWriteError(w, http.StatusBadRequest, "invalid request")
		return
	}

	domain := normalizeDomainCandidate(req.Domain)
	if domain == "" {
		remoteWriteError(w, http.StatusBadRequest, "域名不能为空")
		return
	}

	ctx := r.Context()

	// 两步都要做:
	//  1. 从自定义列表移除 —— 处理用户手工加的域名
	//  2. 写入屏蔽名单 —— 处理主控 URL / 服务器域名 / agent 实配扫出来的域名。
	//     这些来源每次探测都会重新扫出来,不进屏蔽名单的话删了等于没删。
	existing := loadDomainListSetting(ctx, h.repo, realityDomainsSettingKey)
	filtered := make([]string, 0, len(existing))
	for _, d := range existing {
		if d != domain {
			filtered = append(filtered, d)
		}
	}
	_ = saveDomainListSetting(ctx, h.repo, realityDomainsSettingKey, filtered)

	blocked := loadDomainListSetting(ctx, h.repo, realityDomainsBlockedSettingKey)
	if err := saveDomainListSetting(ctx, h.repo, realityDomainsBlockedSettingKey, append(blocked, domain)); err != nil {
		remoteWriteError(w, http.StatusInternalServerError, fmt.Sprintf("写入屏蔽名单失败: %v", err))
		return
	}

	remoteWriteJSON(w, http.StatusOK, map[string]any{"success": true, "message": "已删除"})
}

// HandleListBlockedRealityDomains 返回当前屏蔽名单,供前端展示「已屏蔽」区域。
func (h *RemoteManageHandler) HandleListBlockedRealityDomains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	domains := loadDomainListSetting(r.Context(), h.repo, realityDomainsBlockedSettingKey)
	if domains == nil {
		domains = []string{}
	}
	remoteWriteJSON(w, http.StatusOK, map[string]any{"success": true, "domains": domains})
}

// HandleRestoreRealityDomain 把域名移出屏蔽名单。误删必须能找回,否则用户只能去改数据库。
func (h *RemoteManageHandler) HandleRestoreRealityDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		remoteWriteError(w, http.StatusBadRequest, "invalid request")
		return
	}

	domain := normalizeDomainCandidate(req.Domain)
	if domain == "" {
		remoteWriteError(w, http.StatusBadRequest, "域名不能为空")
		return
	}

	h.unblockRealityDomain(r.Context(), domain)
	remoteWriteJSON(w, http.StatusOK, map[string]any{"success": true, "message": "已恢复"})
}

// unblockRealityDomain 把域名移出屏蔽名单(不存在则无副作用)。
func (h *RemoteManageHandler) unblockRealityDomain(ctx context.Context, domain string) {
	blocked := loadDomainListSetting(ctx, h.repo, realityDomainsBlockedSettingKey)
	filtered := make([]string, 0, len(blocked))
	for _, d := range blocked {
		if normalizeDomainCandidate(d) != domain {
			filtered = append(filtered, d)
		}
	}
	if len(filtered) != len(blocked) {
		_ = saveDomainListSetting(ctx, h.repo, realityDomainsBlockedSettingKey, filtered)
	}
}
