package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"miaomiaowux/internal/storage"
	"miaomiaowux/templates"
)

// deploy_master_proxy.go:「宿主机 agent 反代主控」。
//
// Docker 部署的主控自身开 HTTPS 需要容器内跑 nginx(容器无 systemd,体验差)。更干净的方式:
// 在主控宿主机上装一个 agent,用它的 nginx 对外 listen 443,反代到主控的 http 端口(127.0.0.1:<PORT>)。
// 前提:该 agent 与主控同机(proxy_pass 127.0.0.1 才通),前端仅在「同机 && 主控为 Docker 部署」时显示入口。
//
// 复用:直接沿用主控本机反代面板用的模板 single_nginx.conf + mmwx_domain.conf(后者已是 listen 443 ssl
// → proxy_pass 127.0.0.1:12889,且带 X-Forwarded-Proto https —— 主控据此自动认定已在 HTTPS 后,闭环),
// 经现成的 setup-ssl 通道下发,证书用 WSCertDeployPayload 下发。整体是 deployTunnelConfig 去掉 xray 的简化版。

// deployMasterProxy 在指定(同机)agent 上部署「反代主控」nginx 配置 + 证书。
func (h *RemoteManageHandler) deployMasterProxy(ctx context.Context, server *storage.RemoteServer) error {
	masterDomain := strings.ToLower(strings.TrimSpace(getDomainFromMasterURL(h.repo, ctx)))
	if masterDomain == "" {
		return fmt.Errorf("主控 master_url 未配置域名,无法反代主控;请先在设置里把 master_url 配成 https://你的域名")
	}
	rootDomain := extractRootDomain(masterDomain)

	// 主控面板端口(Docker 容器监听的宿主机端口),默认 12889,与模板里的 proxy_pass 端口一致。
	panelPort := os.Getenv("PORT")
	if panelPort == "" {
		panelPort = "12889"
	}

	// 证书:主控该域名的证书;没有先触发自动签发,让用户稍后重试(签发是异步的)。
	cert, certErr := h.repo.FindDeployableCertByDomain(ctx, rootDomain, server.ID)
	if certErr != nil || cert == nil || cert.CertPEM == "" || cert.KeyPEM == "" {
		if h.certHandler != nil {
			go h.certHandler.DeployAutoDeployCertificates(server.ID)
		}
		return fmt.Errorf("主控域名 %s 尚无可用证书,已触发自动申请,请稍后重试", rootDomain)
	}
	certName := certDeployFilename(cert.Domain)

	// 渲染:主 nginx.conf(含 include servers/*.conf + 80→301)+ 反代 server 块。
	nginxConf, err := templates.ReadFile("single_nginx.conf")
	if err != nil {
		return fmt.Errorf("读取 single_nginx.conf 模板失败: %w", err)
	}
	domainTpl, err := templates.ReadFile("mmwx_domain.conf")
	if err != nil {
		return fmt.Errorf("读取 mmwx_domain.conf 模板失败: %w", err)
	}
	domainConf := strings.ReplaceAll(string(domainTpl), "{domain}", masterDomain)
	domainConf = strings.ReplaceAll(domainConf, "{cert_name}", certName)
	// 模板默认反代 12889;若主控 PORT 改过则替换成实际端口。
	if panelPort != "12889" {
		domainConf = strings.ReplaceAll(domainConf, "127.0.0.1:12889", "127.0.0.1:"+panelPort)
	}

	// 腾出 443(清掉可能占用的 stream 端口),再下发 nginx 配置。
	clearPayload, _ := json.Marshal(map[string]int{"port": 443})
	if _, err := h.forwardToRemoteServer(ctx, server.ID, http.MethodPost, "/api/child/nginx/clear-stream-port", clearPayload); err != nil {
		log.Printf("[ProxyMaster] clear stream port 443 on server %d: %v (non-fatal)", server.ID, err)
	}

	sslPayload, _ := json.Marshal(map[string]any{
		"domain":        masterDomain,
		"nginx_config":  string(nginxConf),
		"domain_config": domainConf,
	})
	if _, err := h.forwardNginxSetupSSL(ctx, server.ID, sslPayload); err != nil {
		return fmt.Errorf("下发 nginx 反代配置失败: %w", err)
	}

	// 下发证书(顺序同 deployTunnelConfig:setup-ssl 先建 cert 目录 + 首次 reload 可能因证书缺失失败但不阻断,
	// 证书落地后 deployToRemoteServer 带 Reload:"nginx" 再 reload 一次即生效)。
	if h.certHandler != nil {
		h.certHandler.deployToRemoteServer(server, WSCertDeployPayload{
			Domain:   rootDomain,
			CertPEM:  cert.CertPEM,
			KeyPEM:   cert.KeyPEM,
			CertPath: fmt.Sprintf("/usr/local/nginx/cert/%s.pem", certName),
			KeyPath:  fmt.Sprintf("/usr/local/nginx/cert/%s.key", certName),
			Reload:   "nginx",
		})
	}

	log.Printf("[ProxyMaster] 已在 server %d (%s) 部署主控反代: %s → 127.0.0.1:%s", server.ID, server.Name, masterDomain, panelPort)
	return nil
}

// HandleProxyMaster POST /api/admin/remote/proxy-master?server_id= —— 在同机 agent 上部署反代主控。
func (h *RemoteManageHandler) HandleProxyMaster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		remoteWriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		remoteWriteError(w, http.StatusBadRequest, "server_id required")
		return
	}
	id, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		remoteWriteError(w, http.StatusBadRequest, "invalid server_id")
		return
	}
	server, err := h.repo.GetRemoteServer(r.Context(), id)
	if err != nil {
		remoteWriteError(w, http.StatusNotFound, "server not found")
		return
	}
	if err := h.deployMasterProxy(r.Context(), server); err != nil {
		remoteWriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "message": "已在该 agent 上部署主控反代,主控域名现在可经该 agent 的 nginx 走 HTTPS 访问"})
}
