package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"miaomiaowux/internal/license"
	"miaomiaowux/internal/storage"
)

// 公告系统:
//   - 模板配置(KV announcement_config):每类型 enabled/标题/文案模板/是否发 bot/是否显示 miniapp。
//   - 公告实例(announcements 表):手动发布或节点被墙自动触发,miniapp 横幅 + bot 广播读它。
//   - 探测源(KV announce_probe_tester_ids):被墙探测从哪些**家用测速端**视角探。
//     机房 agent 探不准被墙,故改用部署在国内家庭网络的测速端。

const (
	AnnouncementConfigKey = "announcement_config"
	// AnnounceProbeServerIDsKey 旧的探测源配置(值是 remote server id)。已废弃 ——
	// agent 都在机房,从机房拨测探不准「被墙」。仅保留 key 名用于启动时检测遗留配置并告警。
	AnnounceProbeServerIDsKey = "announce_probe_server_ids"
	// AnnounceProbeTesterIDsKey 新的探测源配置,值是**家用测速端 id**。
	// 刻意不复用旧 key:语义从 server id 变成 tester id,复用会把旧值当成 tester id 误读。
	AnnounceProbeTesterIDsKey = "announce_probe_tester_ids"
	// AnnounceOfficialProbeKey 是否额外使用官方探测源(PRO)。开启后由许可证服务派发到
	// 官方部署在国内家庭网络的探测端,结果与本地测速端**取并集**(任一可达即可达)。
	AnnounceOfficialProbeKey = "announce_official_probe"
)

// 公告类型
const (
	AnnounceTypeNodeBlocked   = "node_blocked"
	AnnounceTypeNodeRecovered = "node_recovered"
	AnnounceTypeMaintenance   = "maintenance"
	AnnounceTypeSubUpdate     = "sub_update"
	AnnounceTypeGeneral       = "general"
)

type announceTypeConfig struct {
	Enabled    bool   `json:"enabled"`
	Title      string `json:"title"`
	Template   string `json:"template"`
	ViaBot     bool   `json:"via_bot"`
	ViaMiniapp bool   `json:"via_miniapp"`
}

type announceConfig struct {
	Types map[string]announceTypeConfig `json:"types"`
}

// defaultAnnounceConfig 默认模板文案(未配置时生效)。{node}/{time} 运行时替换。
func defaultAnnounceConfig() announceConfig {
	return announceConfig{Types: map[string]announceTypeConfig{
		AnnounceTypeNodeBlocked:   {Enabled: true, Title: "节点异常", Template: "⚠️ 节点【{node}】疑似被墙,暂时无法连接,请先切换其他节点,我们正在处理。", ViaBot: true, ViaMiniapp: true},
		AnnounceTypeNodeRecovered: {Enabled: true, Title: "节点恢复", Template: "✅ 节点【{node}】已恢复,可正常使用。", ViaBot: true, ViaMiniapp: true},
		AnnounceTypeMaintenance:   {Enabled: true, Title: "系统维护", Template: "🛠 系统将于 {time} 维护,期间可能短暂不可用,敬请谅解。", ViaBot: true, ViaMiniapp: true},
		AnnounceTypeSubUpdate:     {Enabled: true, Title: "订阅更新", Template: "🔄 节点有更新,请重新拉取订阅以获取最新节点。", ViaBot: true, ViaMiniapp: true},
		AnnounceTypeGeneral:       {Enabled: true, Title: "公告", Template: "", ViaBot: true, ViaMiniapp: true},
	}}
}

// mergedAnnounceConfig 读 KV 配置,缺失的类型用默认补齐。
func (h *AnnouncementHandler) mergedAnnounceConfig(ctx context.Context) announceConfig {
	cfg := defaultAnnounceConfig()
	raw, _ := h.repo.GetSystemSetting(ctx, AnnouncementConfigKey)
	if strings.TrimSpace(raw) != "" {
		var stored announceConfig
		if json.Unmarshal([]byte(raw), &stored) == nil {
			for k, v := range stored.Types {
				cfg.Types[k] = v
			}
		}
	}
	return cfg
}

type AnnouncementHandler struct {
	repo *storage.TrafficRepository
	// license 用于官方探测源的 PRO 门控与请求下发;为 nil 时官方探测源不可用。
	license *license.Manager
}

func NewAnnouncementHandler(repo *storage.TrafficRepository, lic *license.Manager) *AnnouncementHandler {
	return &AnnouncementHandler{repo: repo, license: lic}
}

// ===== 模板配置(admin) GET/PUT /api/admin/system-settings/announcements =====

func (h *AnnouncementHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.mergedAnnounceConfig(r.Context())
	probeIDs := h.probeTesterIDs(r.Context())
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true, "config": cfg, "probe_tester_ids": probeIDs,
		"official_probe": h.officialProbeEnabled(r.Context()),
		// 前端据此决定「官方探测(PRO)」开关是否可用
		"official_probe_available": h.license != nil && h.license.HasFeature(license.FeatureSpeedTest),
	})
}

func (h *AnnouncementHandler) SetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config         announceConfig `json:"config"`
		ProbeTesterIDs *[]int64       `json:"probe_tester_ids"`
		OfficialProbe  *bool          `json:"official_probe"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	b, _ := json.Marshal(req.Config)
	if err := h.repo.SetSystemSetting(r.Context(), AnnouncementConfigKey, string(b)); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.ProbeTesterIDs != nil {
		ids, _ := json.Marshal(*req.ProbeTesterIDs)
		_ = h.repo.SetSystemSetting(r.Context(), AnnounceProbeTesterIDsKey, string(ids))
	}
	if req.OfficialProbe != nil {
		v := "0"
		if *req.OfficialProbe {
			v = "1"
		}
		_ = h.repo.SetSystemSetting(r.Context(), AnnounceOfficialProbeKey, v)
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "message": "公告配置已更新"})
}

// probeTesterIDs 返回被选作被墙探测源的家用测速端 id 列表。
func (h *AnnouncementHandler) probeTesterIDs(ctx context.Context) []int64 {
	raw, _ := h.repo.GetSystemSetting(ctx, AnnounceProbeTesterIDsKey)
	out := []int64{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// officialProbeEnabled 是否启用官方探测源(PRO)。只读配置,PRO 校验在实际探测时做 ——
// 许可证可能在配置之后过期,此处返回 true 不代表这一轮真能用。
func (h *AnnouncementHandler) officialProbeEnabled(ctx context.Context) bool {
	v, _ := h.repo.GetSystemSetting(ctx, AnnounceOfficialProbeKey)
	return v == "1"
}

// WarnLegacyProbeSource 启动时检查:旧的 agent 探测源有值、而新的测速端探测源为空 →
// 说明这是一个从旧版本升上来、还没重新配过的实例,被墙探测会静默停用。打一条明确的
// WARN 提示重新配置 —— 不自动迁移,因为 server id 和 tester id 之间没有任何对应关系。
func (h *AnnouncementHandler) WarnLegacyProbeSource(ctx context.Context) {
	legacy, _ := h.repo.GetSystemSetting(ctx, AnnounceProbeServerIDsKey)
	if strings.TrimSpace(legacy) == "" || strings.TrimSpace(legacy) == "[]" {
		return
	}
	if len(h.probeTesterIDs(ctx)) > 0 {
		return
	}
	log.Printf("[Announce] 被墙探测已停用:探测源改用「家用测速端」,但当前未选择任何测速端"+
		"(检测到旧的 agent 探测源配置 %s)。请在 系统设置 → 公告 里重新选择测速端作为探测源,"+
		"并确保测速端已升级到支持可达性探测的版本。", legacy)
}

// ===== 公告实例(admin) /api/admin/announcements =====

func (h *AnnouncementHandler) ServeAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listInstances(w, r)
	case http.MethodPost:
		h.createInstance(w, r)
	case http.MethodDelete:
		h.deleteInstance(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, errors.New("方法不允许"))
	}
}

func (h *AnnouncementHandler) listInstances(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ListActiveAnnouncements(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "announcements": items})
}

func (h *AnnouncementHandler) createInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type           string `json:"type"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		ExpiresMinutes int    `json:"expires_minutes"` // 0 = 永不过期
		ViaBot         bool   `json:"via_bot"`
		ViaMiniapp     bool   `json:"via_miniapp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("请求格式错误"))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeError(w, http.StatusBadRequest, errors.New("公告正文不能为空"))
		return
	}
	if req.Type == "" {
		req.Type = AnnounceTypeGeneral
	}
	var expiresAt *time.Time
	if req.ExpiresMinutes > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresMinutes) * time.Minute)
		expiresAt = &t
	}
	id, err := h.PublishAnnouncement(r.Context(), storage.Announcement{
		Type: req.Type, Title: strings.TrimSpace(req.Title), Body: strings.TrimSpace(req.Body),
		ViaBot: req.ViaBot, ViaMiniapp: req.ViaMiniapp, ExpiresAt: expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "id": id})
}

func (h *AnnouncementHandler) deleteInstance(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("无效的公告 id"))
		return
	}
	if err := h.repo.DeleteAnnouncement(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true})
}

// PublishAnnouncement 插入一条公告实例(手动 / 自动触发 / tgbot 命令共用)。
func (h *AnnouncementHandler) PublishAnnouncement(ctx context.Context, a storage.Announcement) (int64, error) {
	id, err := h.repo.CreateAnnouncement(ctx, a)
	if err != nil {
		log.Printf("[公告广播] 创建公告失败: type=%s via_bot=%v via_miniapp=%v err=%v", a.Type, a.ViaBot, a.ViaMiniapp, err)
		return id, err
	}
	// 关键诊断:via_bot=false 的公告 bot 永远不会推送(收不到 TG 的一大真因)。
	// 排查「收不到 TG」时,先看这条日志确认 via_bot 是否为 true;再看 tgbot_admin 的「bot 轮询到待推送公告」日志确认收件人数。
	log.Printf("[公告广播] 已创建公告 id=%d type=%s via_bot=%v via_miniapp=%v", id, a.Type, a.ViaBot, a.ViaMiniapp)
	return id, nil
}

// ===== 生效公告(authenticated) GET /api/announcements/active =====
// 供 Web 前端横幅;miniapp 走 tgbot 转发的只读版。

func (h *AnnouncementHandler) GetActive(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ListActiveAnnouncements(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "announcements": items})
}

// filterAnnouncementsForUser 按用户过滤公告(miniapp 横幅用):
//   - 无生效套餐的用户 → 一条都不显示(公告只面向有套餐用户);
//   - 节点相关公告(node_id!=0)→ 仅当用户套餐内含该节点才显示。
func filterAnnouncementsForUser(ctx context.Context, repo *storage.TrafficRepository, username string, items []storage.Announcement) []storage.Announcement {
	empty := []storage.Announcement{}
	if strings.TrimSpace(username) == "" {
		return empty
	}
	user, err := repo.GetUser(ctx, username)
	if err != nil || user.PackageID <= 0 {
		return empty
	}
	if user.PackageEndDate != nil && !user.PackageEndDate.After(time.Now()) {
		return empty // 套餐已过期
	}
	nodeSet := map[int64]bool{}
	if pkg, perr := repo.GetPackage(ctx, user.PackageID); perr == nil && pkg != nil {
		for _, nid := range pkg.Nodes {
			nodeSet[nid] = true
		}
	}
	out := make([]storage.Announcement, 0, len(items))
	for _, a := range items {
		if a.NodeID != 0 && !nodeSet[a.NodeID] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GetBlockedNodes GET /api/admin/announcements/blocked-nodes:被墙节点 id 列表(供节点列表徽章)。
func (h *AnnouncementHandler) GetBlockedNodes(w http.ResponseWriter, r *http.Request) {
	set, err := h.repo.ListBlockedNodeIDs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	respondJSON(w, http.StatusOK, map[string]any{"success": true, "node_ids": ids})
}
