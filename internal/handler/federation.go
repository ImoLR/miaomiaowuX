package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"miaomiaowux/internal/license"
	"miaomiaowux/internal/securechan"
	"miaomiaowux/internal/storage"
)

// 联邦(服务器分享)入口:其他妙妙屋X主控凭分享令牌,通过本主控间接管理被分享的服务器。
// 鉴权用分享令牌(X-Share-Token),不走 JWT。本主控始终是 agent 的唯一控制者,
// 所有转发都经 RemoteManageHandler 走 securechan 到 agent。
//
// 消费方↔拥有方这一跳在 HTTPS 之上叠加"令牌揭示的 ECDH"端到端加密(见 federation_crypto.go),
// 拥有方不支持时消费方自动降级为明文+令牌。

type FederationHandler struct {
	repo         *storage.TrafficRepository
	remoteManage *RemoteManageHandler
	license      *license.Manager
	sessions     *securechan.SessionCache // 拥有方侧:按分享令牌缓存与消费方的会话
	probeStore   *ProbeMetricsStore       // 拥有方侧探针指标(cpu/mem/disk),server-info 透传给消费方
}

func NewFederationHandler(repo *storage.TrafficRepository, remoteManage *RemoteManageHandler, lic *license.Manager) *FederationHandler {
	return &FederationHandler{repo: repo, remoteManage: remoteManage, license: lic, sessions: securechan.NewSessionCache(30 * time.Minute)}
}

// SetProbeStore 注入拥有方探针指标存储(probeStore 在 main 里晚于本 handler 创建,故用 setter)。
func (h *FederationHandler) SetProbeStore(s *ProbeMetricsStore) { h.probeStore = s }

func (h *FederationHandler) proEnabled() bool {
	if h.license == nil {
		return false
	}
	// Manager.HasFeature 才走完整的 ed25519 token 验签(Status.HasFeature 已废弃恒返 false 防绕过)。
	return h.license.HasFeature(featureServerShare)
}

// authShare 校验分享令牌,返回被分享的 server_id。
func (h *FederationHandler) authShare(r *http.Request) (int64, error) {
	token := strings.TrimSpace(r.Header.Get("X-Share-Token"))
	if token == "" {
		// 兼容 Authorization: Bearer
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
		}
	}
	if token == "" {
		return 0, errors.New("missing share token")
	}
	share, err := h.repo.GetSharedServerByTokenHash(r.Context(), hashShareToken(token))
	if err != nil {
		return 0, errors.New("invalid or revoked share token")
	}
	return share.ServerID, nil
}

func (h *FederationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.proEnabled() {
		writeError(w, http.StatusForbidden, errors.New("server share (PRO) not enabled on owner"))
		return
	}
	serverID, err := h.authShare(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	switch r.URL.Path {
	case "/api/federation/manage":
		h.handleManage(w, r, serverID)
	case "/api/federation/server-info":
		h.handleServerInfo(w, r, serverID)
	default:
		writeError(w, http.StatusNotFound, errors.New("not found"))
	}
}

// handleManage 转发一条 agent 管理命令(消费方指定 method/path/body,path 限定 /api/child/)。
// 在 HTTPS 之上叠加令牌揭示的 ECDH 端到端加密:支持密钥交换、加密请求、明文降级三种形态。
func (h *FederationHandler) handleManage(w http.ResponseWriter, r *http.Request, serverID int64) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}

	payload, session, err := h.negotiateManage(w, r)
	if err != nil || payload == nil {
		return // negotiateManage 已写错误响应
	}

	var req struct {
		Method  string `json:"method"`
		Path    string `json:"path"`
		BodyB64 string `json:"body"` // base64,可空
	}
	if jerr := json.Unmarshal(payload, &req); jerr != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request"))
		return
	}
	if !strings.HasPrefix(req.Path, "/api/child/") {
		writeError(w, http.StatusBadRequest, errors.New("path must be under /api/child/"))
		return
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	var body []byte
	if req.BodyB64 != "" {
		b, derr := base64.StdEncoding.DecodeString(req.BodyB64)
		if derr != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid body encoding"))
			return
		}
		body = b
	}

	// 作用域校验:默认(allow_manage_xray=false)只让接收方管自己经本分享建的入站,
	// 拒绝拉全量入站/配置反推其他用户链接。allow_manage_xray=true 则全权放行。
	share, sErr := h.repo.GetSharedServerByTokenHash(r.Context(), hashShareToken(fedToken(r)))
	if sErr != nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid or revoked share token"))
		return
	}
	if !share.AllowManageXray {
		if deny := h.scopeFederationCommand(r.Context(), share.ID, req.Method, req.Path, body); deny != "" {
			writeError(w, http.StatusForbidden, errors.New(deny))
			return
		}
	}

	result, ferr := h.remoteManage.ForwardToAgent(r.Context(), serverID, req.Method, req.Path, body)
	if ferr != nil {
		writeError(w, http.StatusBadGateway, ferr)
		return
	}
	// 联邦入站溯源:接收方经本分享加/删入站时记录/删除,供吊销分享时删 agent 入站(让接收方节点随之失效)。
	h.trackFederationInbound(r, serverID, req.Method, req.Path, body)

	// 作用域下:GET 入站列表只回本分享自己建的入站,隐藏他人入站(防反推链接)。
	if !share.AllowManageXray && req.Method == http.MethodGet && stripQuery(req.Path) == "/api/child/inbounds" {
		result = h.filterInboundsForShare(r.Context(), share.ID, result)
	}

	if session != nil {
		if enc, eerr := session.Encrypt(result); eerr == nil {
			w.Header().Set("X-Encrypted", "1")
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(enc)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// trackFederationInbound 解析接收方经联邦转发的入站命令,记录/删除入站溯源。
// 加入站(action add / 空)→ 记 inbound.tag;删入站(action remove)→ 删 tag。非入站命令忽略。
func (h *FederationHandler) trackFederationInbound(r *http.Request, serverID int64, method, path string, body []byte) {
	if method != http.MethodPost {
		return
	}
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	if path != "/api/child/inbounds" {
		return
	}
	var inb struct {
		Action  string `json:"action"`
		Tag     string `json:"tag"`
		Inbound struct {
			Tag string `json:"tag"`
		} `json:"inbound"`
	}
	if json.Unmarshal(body, &inb) != nil {
		return
	}
	tag := inb.Tag
	if tag == "" {
		tag = inb.Inbound.Tag
	}
	if tag == "" {
		return
	}
	share, err := h.repo.GetSharedServerByTokenHash(r.Context(), hashShareToken(fedToken(r)))
	if err != nil {
		return
	}
	switch strings.ToLower(inb.Action) {
	case "", "add":
		_ = h.repo.RecordSharedInbound(r.Context(), share.ID, serverID, tag)
	case "remove":
		_ = h.repo.UnrecordSharedInbound(r.Context(), share.ID, tag)
	}
}

// stripQuery 去掉 path 的 querystring。
func stripQuery(path string) string {
	if i := strings.Index(path, "?"); i >= 0 {
		return path[:i]
	}
	return path
}

// scopeFederationCommand 作用域校验:allow_manage_xray=false 的分享只允许接收方管理自己经本分享建的入站。
// 返回非空 = 拒绝原因。允许:GET /api/child/inbounds(结果再过滤)、POST /api/child/inbounds 的 add;
// remove/update 仅限本分享自己的 tag;其余 /api/child/*(xray/config、system-config、outbounds 等)一律拒绝。
func (h *FederationHandler) scopeFederationCommand(ctx context.Context, shareID int64, method, path string, body []byte) string {
	switch stripQuery(path) {
	case "/api/child/inbounds":
		if method == http.MethodGet {
			return "" // 允许,响应稍后按本分享 tag 过滤
		}
		if method == http.MethodPost {
			var b struct {
				Action  string `json:"action"`
				Tag     string `json:"tag"`
				Inbound struct {
					Tag string `json:"tag"`
				} `json:"inbound"`
			}
			_ = json.Unmarshal(body, &b)
			switch strings.ToLower(b.Action) {
			case "", "add":
				return "" // 加自己的入站,允许(由 trackFederationInbound 溯源)
			default: // remove / update:tag 必须属于本分享
				tag := b.Tag
				if tag == "" {
					tag = b.Inbound.Tag
				}
				tags, _ := h.repo.ListSharedInboundTags(ctx, shareID)
				for _, t := range tags {
					if t == tag {
						return ""
					}
				}
				return "此分享只能操作自己创建的入站"
			}
		}
		return "此分享无权进行该操作"
	default:
		return "此分享无权查看/修改完整 Xray 配置(仅可管理自己创建的入站);如需完整权限请让拥有方在分享时勾选允许"
	}
}

// filterInboundsForShare 把 GET /api/child/inbounds 的响应过滤到只含本分享自己建的入站,隐藏他人入站。
// 解析失败/出错时保守返回"空入站列表",绝不泄露原始全量。
func (h *FederationHandler) filterInboundsForShare(ctx context.Context, shareID int64, result []byte) []byte {
	empty, _ := json.Marshal(map[string]any{"success": true, "inbounds": []any{}})
	tags, err := h.repo.ListSharedInboundTags(ctx, shareID)
	if err != nil {
		return empty
	}
	allow := make(map[string]bool, len(tags))
	for _, t := range tags {
		allow[t] = true
	}
	var obj map[string]any
	if json.Unmarshal(result, &obj) != nil {
		return empty
	}
	arr, ok := obj["inbounds"].([]any)
	if !ok {
		return empty
	}
	filtered := make([]any, 0, len(arr))
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if tag, _ := m["tag"].(string); allow[tag] {
			filtered = append(filtered, it)
		}
	}
	obj["inbounds"] = filtered
	out, merr := json.Marshal(obj)
	if merr != nil {
		return empty
	}
	return out
}

// fedToken 取分享令牌(与 authShare 一致),用作会话缓存键。
func fedToken(r *http.Request) string {
	token := strings.TrimSpace(r.Header.Get("X-Share-Token"))
	if token == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
		}
	}
	return token
}

// negotiateManage 处理握手/解密,返回明文 payload 及用于加密响应的 session(明文请求时为 nil)。
func (h *FederationHandler) negotiateManage(w http.ResponseWriter, r *http.Request) ([]byte, *securechan.Session, error) {
	token := fedToken(r)
	body, _ := io.ReadAll(r.Body)

	// 1. 密钥交换:消费方带临时公钥,拥有方生成自己的临时对并派生会话(令牌揭示),回带公钥。
	if kx := r.Header.Get(fedKeyExchangeHeader); kx != "" {
		consPub, ok := decodeKey(kx)
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("invalid key exchange"))
			return nil, nil, errBadKeyExchange
		}
		ownerPriv, ownerPub, gerr := securechan.GenerateEphemeral()
		if gerr != nil {
			writeError(w, http.StatusInternalServerError, errors.New("key generation failed"))
			return nil, nil, gerr
		}
		session, derr := deriveFederationSession(ownerPriv, ownerPub, consPub, token, false)
		if derr != nil {
			writeError(w, http.StatusInternalServerError, errors.New("session derivation failed"))
			return nil, nil, derr
		}
		if token != "" {
			h.sessions.Set(token, session)
		}
		w.Header().Set(fedKeyExchangeHeader, encodeKey(ownerPub))
		// 握手阶段请求体为明文,响应也明文返回(消费方此时尚未持有会话)。
		return body, nil, nil
	}

	// 2. 加密请求:用缓存会话解密;无会话要求重新协商。
	if r.Header.Get("X-Encrypted") == "1" {
		session := h.sessions.Get(token)
		if session == nil {
			writeError(w, http.StatusPreconditionFailed, errors.New("no session, re-negotiate"))
			return nil, nil, errNoSession
		}
		plain, derr := session.Decrypt(body)
		if derr != nil {
			writeError(w, http.StatusBadRequest, errors.New("decrypt failed"))
			return nil, nil, derr
		}
		return plain, session, nil
	}

	// 3. 明文请求(自动降级)。
	return body, nil, nil
}

var (
	errBadKeyExchange = errors.New("invalid key exchange")
	errNoSession      = errors.New("no session")
)

// handleServerInfo 返回被分享服务器的状态/流量快照(由拥有方主控采集,透传给消费方)。
func (h *FederationHandler) handleServerInfo(w http.ResponseWriter, r *http.Request, serverID int64) {
	srv, err := h.repo.GetRemoteServer(r.Context(), serverID)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("server not found"))
		return
	}
	trafficUsed, _ := h.repo.GetServerTrafficUsed(r.Context(), serverID)
	resp := map[string]any{
		"name":       srv.Name,
		"status":     srv.Status,
		"ip_address": srv.IPAddress,
		// v6 网络信息:透传给消费方,让分享服务器也能建 IPv6 节点(消费方本地无 agent、拿不到这些)。
		"ip_address_v6": srv.IPAddressV6,
		"ipv6_enabled":  srv.IPv6Enabled,
		"domain":        srv.Domain,
		"domain_v6":     srv.DomainV6,
		"xray_mode":     srv.XrayMode,
		"traffic_limit":          srv.TrafficLimit,
		"traffic_reset_day":      srv.TrafficResetDay,
		"traffic_used":           trafficUsed + srv.TrafficUsedOffset,
		"current_upload_speed":   srv.CurrentUploadSpeed,
		"current_download_speed": srv.CurrentDownloadSpeed,
		"xray_running":           srv.XrayRunning,
		"xray_version":           srv.XrayVersion,
		"last_heartbeat":         srv.LastHeartbeat,
	}
	// 探针系统指标(cpu/mem/disk/loadavg):拥有方从 agent WS 上报里拿到,透传给消费方,
	// 让分享服务器在接收方探针里数据完整(消费方本地无 agent WS,不透传就只有速率/流量)。
	if h.probeStore != nil {
		if view, ok := h.probeStore.Snapshot(serverID, 0); ok && view.HasSys {
			s := view.Sys
			resp["probe_sys"] = map[string]any{
				"cpu_pct":    s.CPUPct,
				"loadavg":    s.LoadAvg,
				"mem_used":   s.MemUsed,
				"mem_total":  s.MemTotal,
				"disk_used":  s.DiskUsed,
				"disk_total": s.DiskTotal,
				"has_cpu":    s.HasCPU,
				"has_mem":    s.HasMem,
				"has_disk":   s.HasDisk,
				"at":         s.At,
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
