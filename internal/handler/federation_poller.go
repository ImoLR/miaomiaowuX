package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"miaomiaowux/internal/license"
	"miaomiaowux/internal/storage"
	"miaomiaowux/internal/version"
)

// 消费方:定时从拥有方主控拉取被分享服务器的状态/流量快照,写回本地 remote_servers 行,
// 让分享服务器在服务管理列表里像普通服务器一样显示速率、流量、心跳。
// 拥有方那一跳依赖 HTTPS;后续可叠加 securechan 端到端加密。

const federationPollInterval = 5 * time.Second

// federationLicense 联邦轮询的 license 引用(由 main wire 时 SetFederationLicense 注入)。
// 散布校验:消费方拉取被分享 server 信息也是 server_share PRO 功能的运行时路径。
var federationLicense *license.Manager

// SetFederationLicense 注入联邦校验所需的 license 管理器。
func SetFederationLicense(lic *license.Manager) {
	federationLicense = lic
}

func StartFederationPoller(ctx context.Context, repo *storage.TrafficRepository, probeStore *ProbeMetricsStore) {
	client := &http.Client{Timeout: 15 * time.Second}
	ticker := time.NewTicker(federationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollFederatedServers(ctx, repo, client, probeStore)
		}
	}
}

func pollFederatedServers(ctx context.Context, repo *storage.TrafficRepository, client *http.Client, probeStore *ProbeMetricsStore) {
	// 散布验签:无 license 直接跳过整轮 ticker。
	if federationLicense != nil && !federationLicense.HasFeature("server_share") {
		return
	}
	feds, err := repo.ListFederatedServers(ctx)
	if err != nil {
		return
	}
	for _, fed := range feds {
		info, err := fetchFederationServerInfo(ctx, client, fed)
		if err != nil {
			continue
		}
		applyFederationInfo(ctx, repo, fed.ServerID, info)
		ingestFederationProbeSys(fed.ServerID, info, probeStore)
	}
}

// ingestFederationProbeSys 把拥有方透传的探针系统指标(probe_sys)写进本地 probeStore,
// 让分享服务器在接收方探针页(HTTP + WS 两端都读 probeStore)里 cpu/mem/disk/load 数据完整。
func ingestFederationProbeSys(serverID int64, info map[string]any, probeStore *ProbeMetricsStore) {
	if probeStore == nil {
		return
	}
	ps, ok := info["probe_sys"].(map[string]any)
	if !ok {
		return
	}
	loadavg, _ := ps["loadavg"].(string)
	snap := ProbeSysSnapshot{
		CPUPct:    jsonFloat(ps["cpu_pct"]),
		LoadAvg:   loadavg,
		MemUsed:   jsonInt(ps["mem_used"]),
		MemTotal:  jsonInt(ps["mem_total"]),
		DiskUsed:  jsonInt(ps["disk_used"]),
		DiskTotal: jsonInt(ps["disk_total"]),
		HasCPU:    jsonBool(ps["has_cpu"]),
		HasMem:    jsonBool(ps["has_mem"]),
		HasDisk:   jsonBool(ps["has_disk"]),
		At:        jsonInt(ps["at"]),
	}
	probeStore.IngestSys(serverID, snap)
}

func fetchFederationServerInfo(ctx context.Context, client *http.Client, fed storage.FederatedServer) (map[string]any, error) {
	url := strings.TrimRight(fed.OwnerURL, "/") + "/api/federation/server-info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Share-Token", fed.ShareToken)
	req.Header.Set("User-Agent", version.AgentUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errFederationInfo
	}
	var info map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return info, nil
}

var errFederationInfo = &federationError{"federation server-info error"}

type federationError struct{ msg string }

func (e *federationError) Error() string { return e.msg }

func applyFederationInfo(ctx context.Context, repo *storage.TrafficRepository, serverID int64, info map[string]any) {
	up := jsonInt(info["current_upload_speed"])
	down := jsonInt(info["current_download_speed"])
	_ = repo.UpdateRemoteServerSpeed(ctx, serverID, up, down)

	// 联邦服务器本地无节点流量,用 offset 承载拥有方透传的已用流量
	_ = repo.UpdateRemoteServerTrafficOffset(ctx, serverID, jsonInt(info["traffic_used"]))

	// 透传拥有方的流量限额与重置日(否则消费方显示"不限流量")
	_ = repo.UpdateRemoteServerTrafficMeta(ctx, serverID, jsonInt(info["traffic_limit"]), int(jsonInt(info["traffic_reset_day"])))

	if running, ok := info["xray_running"].(bool); ok {
		ver, _ := info["xray_version"].(string)
		prev, uErr := repo.UpdateRemoteServerXrayStatus(ctx, serverID, running, ver)
		// 联邦拉取的 xray 状态翻转同样发通知;联邦消费方角度也是"这台服务器的 xray 状态变了"
		if uErr == nil && prev != running {
			if server, gErr := repo.GetRemoteServer(ctx, serverID); gErr == nil && server != nil {
				SendXrayStatusChangeNotification(ctx, server.Name, server.IPAddress, running)
			}
		}
	}

	// 透传拥有方的 xray 模式(embedded/external),否则消费方一直显示默认的"外置"
	if mode, _ := info["xray_mode"].(string); mode != "" {
		_ = repo.UpdateRemoteServerXrayMode(ctx, serverID, mode)
	}

	// 透传拥有方的 v6 网络信息(ip_address_v6 / domain / domain_v6 / ipv6_enabled),
	// 否则分享服务器无 v6、其节点无法走 IPv6。空值不覆盖旧值。
	ipv6, _ := info["ip_address_v6"].(string)
	domain, _ := info["domain"].(string)
	domainV6, _ := info["domain_v6"].(string)
	ipv6Enabled, _ := info["ipv6_enabled"].(bool)
	_ = repo.UpdateRemoteServerV6Info(ctx, serverID, ipv6, domain, domainV6, ipv6Enabled)

	// 拥有方报告 connected 时刷新心跳/状态;联邦消费侧也补上离线→在线的 TG 通知
	if st, _ := info["status"].(string); st == "connected" {
		prev, name, ip, prevNotified, _ := repo.UpdateRemoteServerLastActivity(ctx, serverID)
		// 与容忍阈值一致:只有下线通知已发过(离线满阈值)才补发上线通知;阈值内恢复保持静默。
		if prev == storage.RemoteServerStatusOffline && name != "" && prevNotified {
			SendServerOnlineNotification(ctx, name, ip)
		}
	}
}

func jsonInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func jsonFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}

func jsonBool(v any) bool {
	b, _ := v.(bool)
	return b
}
