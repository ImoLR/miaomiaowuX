package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"miaomiaowux/internal/license"
	"miaomiaowux/internal/storage"
)

// 服务器分享(PRO):拥有方为某台 remote_server 生成/管理分享令牌,
// 其他妙妙屋X主控凭令牌通过 /api/federation/* 间接管理该服务器。
//
// server_share 这个 PRO 能力通过 license features "server_share" 控制。

const featureServerShare = "server_share"

type ServerShareHandler struct {
	repo         *storage.TrafficRepository
	license      *license.Manager
	remoteManage *RemoteManageHandler // 吊销时删接收方经联邦创建的 agent 入站
}

func NewServerShareHandler(repo *storage.TrafficRepository, lic *license.Manager, rm *RemoteManageHandler) *ServerShareHandler {
	return &ServerShareHandler{repo: repo, license: lic, remoteManage: rm}
}

func hashShareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *ServerShareHandler) proEnabled() bool {
	if h.license == nil {
		return false
	}
	// 同 federation.go:同名方法但用 Manager.HasFeature 走完整验签。
	return h.license.HasFeature(featureServerShare)
}

func (h *ServerShareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.proEnabled() {
		writeError(w, http.StatusForbidden, errors.New("服务器分享是 PRO 功能，请升级许可证"))
		return
	}

	switch {
	case r.URL.Path == "/api/admin/server-share/create" && r.Method == http.MethodPost:
		h.handleCreate(w, r)
	case r.URL.Path == "/api/admin/server-share/list" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case r.URL.Path == "/api/admin/server-share/revoke" && r.Method == http.MethodPost:
		h.handleRevoke(w, r)
	default:
		writeError(w, http.StatusNotFound, errors.New("not found"))
	}
}

func (h *ServerShareHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID int64  `json:"server_id"`
		Label    string `json:"label"`
		// AllowManageXray 允许接收方查看/修改完整 Xray 配置(全权)。默认 false = 只能管自己经本分享建的入站,
		// 防止接收方拉全量入站/配置反推其他用户的连接链接。
		AllowManageXray bool `json:"allow_manage_xray"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ServerID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("server_id required"))
		return
	}
	// 校验服务器存在
	if _, err := h.repo.GetRemoteServer(r.Context(), req.ServerID); err != nil {
		writeError(w, http.StatusNotFound, errors.New("server not found"))
		return
	}

	token, err := generateSecureToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id, err := h.repo.CreateSharedServer(r.Context(), req.ServerID, hashShareToken(token), req.Label, req.AllowManageXray)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// share_token 只在创建时明文返回一次
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          id,
		"server_id":   req.ServerID,
		"share_token": token,
	})
}

func (h *ServerShareHandler) handleList(w http.ResponseWriter, r *http.Request) {
	serverID, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	if serverID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("server_id required"))
		return
	}
	shares, err := h.repo.ListSharedServers(r.Context(), serverID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"shares": shares})
}

func (h *ServerShareHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
		// DeleteInbounds 吊销时是否删掉接收方经本分享在 agent 上创建的入站(让其节点随之失效)。
		// 前端确认默认勾选(true);nil 时保守=不删(兼容其它调用方)。
		DeleteInbounds *bool `json:"delete_inbounds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("id required"))
		return
	}

	// 吊销前:若选择删入站,把该分享溯源到的 agent 入站逐个删掉(经 owner→agent 直连,不依赖分享令牌)。
	deleted := 0
	if req.DeleteInbounds != nil && *req.DeleteInbounds && h.remoteManage != nil {
		if serverID, serr := h.repo.GetSharedServerServerID(r.Context(), req.ID); serr == nil && serverID > 0 {
			tags, _ := h.repo.ListSharedInboundTags(r.Context(), req.ID)
			for _, tag := range tags {
				body, _ := json.Marshal(map[string]any{"action": "remove", "tag": tag})
				if _, ferr := h.remoteManage.ForwardToAgent(r.Context(), serverID, http.MethodPost, "/api/child/inbounds", body); ferr == nil {
					deleted++
				}
			}
		}
	}
	// 溯源记录清掉(无论是否删入站,吊销后该分享的溯源都无意义)。
	_ = h.repo.ClearSharedInbounds(r.Context(), req.ID)

	if err := h.repo.RevokeSharedServer(r.Context(), req.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "revoked", "inbounds_deleted": deleted})
}
