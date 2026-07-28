package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"miaomiaowux/internal/auth"
	"miaomiaowux/internal/storage"
)

type RuleTemplatesHandler struct {
	repo *storage.TrafficRepository
}

func NewRuleTemplatesHandler(repo *storage.TrafficRepository) *RuleTemplatesHandler {
	return &RuleTemplatesHandler{repo: repo}
}

// canModifyRuleTemplate 判断当前用户能否修改/删除指定模板:
// 管理员任意;普通用户仅限自己上传的(归属为空的历史模板视为管理员所有,普通用户不可动)。
func (h *RuleTemplatesHandler) canModifyRuleTemplate(r *http.Request, filename string) bool {
	username := auth.UsernameFromContext(r.Context())
	if h.repo == nil {
		return true
	}
	if userIsAdmin(r.Context(), h.repo, username) {
		return true
	}
	owner, _ := h.repo.GetRuleTemplateOwner(r.Context(), filename)
	return owner != "" && owner == username
}

const (
	ruleTemplateMaxCount    = 200     // rule_templates 目录最多文件数
	ruleTemplateMaxFileSize = 2 << 20 // 单个模板文件最大 2MB
)

// settingUserHiddenRuleTemplates 存管理员配置的「对普通用户隐藏的内置(无归属)模板」文件名 JSON 数组。
// 内置模板默认对所有用户可见;管理员把某个加入此列表后,普通用户列表里就不再显示它(不影响管理员)。
const settingUserHiddenRuleTemplates = "user_hidden_rule_templates"

// loadHiddenRuleTemplates 读隐藏集(文件名 → true)。空 / 解析失败均返回空集(=全部内置可见)。
func (h *RuleTemplatesHandler) loadHiddenRuleTemplates(r *http.Request) map[string]bool {
	set := map[string]bool{}
	if h.repo == nil {
		return set
	}
	raw, _ := h.repo.GetSystemSetting(r.Context(), settingUserHiddenRuleTemplates)
	if strings.TrimSpace(raw) == "" {
		return set
	}
	var arr []string
	if json.Unmarshal([]byte(raw), &arr) == nil {
		for _, fn := range arr {
			set[fn] = true
		}
	}
	return set
}

// isRuleTemplateFile 判断文件名是否为受支持的模板文件。
// .yaml/.yml → Clash V3 模板;.conf → Surge 模板(前端按扩展名区分类型与编辑器)。
func isRuleTemplateFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".conf")
}

// countRuleTemplates 统计 rule_templates 目录下的模板文件数量。
func countRuleTemplates(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && isRuleTemplateFile(e.Name()) {
			n++
		}
	}
	return n
}

func (h *RuleTemplatesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 删除 /api/rule-templates 前缀
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/rule-templates")

	switch {
	case path == "" || path == "/":
		// 列出模板
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleListTemplates(w, r)
	case path == "/upload":
		// 上传模板
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleUploadTemplate(w, r)
	case path == "/visibility":
		h.handleVisibility(w, r)
	case path == "/rename":
		// 重命名模板
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleRenameTemplate(w, r)
	default:
		// 从路径中提取模板名称（删除前导斜杠）
		templateName := strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodGet:
			// 获取具体模板内容(所有用户可读,用于使用模板)
			h.handleGetTemplate(w, r, templateName)
		case http.MethodPut:
			// 更新模板内容:仅管理员或模板所有者
			if !h.canModifyRuleTemplate(r, templateName) {
				http.Error(w, "无权修改该模板", http.StatusForbidden)
				return
			}
			h.handleUpdateTemplate(w, r, templateName)
		case http.MethodDelete:
			// 删除模板:仅管理员或模板所有者
			if !h.canModifyRuleTemplate(r, templateName) {
				http.Error(w, "无权删除该模板", http.StatusForbidden)
				return
			}
			h.handleDeleteTemplate(w, r, templateName)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleVisibility 管理员配置「哪些内置(无归属)模板对普通用户可见」。仅管理员可调。
// GET  → { builtins: [...全部内置模板文件名], hidden: [...当前对用户隐藏的] }
// PUT  → body { hidden: [...] } 覆盖隐藏集。
func (h *RuleTemplatesHandler) handleVisibility(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFromContext(r.Context())
	if !userIsAdmin(r.Context(), h.repo, username) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := os.ReadDir("rule_templates")
		if err != nil {
			http.Error(w, "Failed to read templates directory", http.StatusInternalServerError)
			return
		}
		allOwners, _ := h.repo.ListRuleTemplateOwners(r.Context())
		builtins := make([]string, 0)
		for _, e := range entries {
			if e.IsDir() || !isRuleTemplateFile(e.Name()) {
				continue
			}
			// 只列内置(无归属)模板 —— 用户私有模板本就只对其本人可见,不在此配置范围。
			if _, owned := allOwners[e.Name()]; !owned {
				builtins = append(builtins, e.Name())
			}
		}
		hidden := h.loadHiddenRuleTemplates(r)
		hiddenArr := make([]string, 0, len(hidden))
		for fn := range hidden {
			hiddenArr = append(hiddenArr, fn)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"builtins": builtins, "hidden": hiddenArr})

	case http.MethodPut:
		var req struct {
			Hidden []string `json:"hidden"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if req.Hidden == nil {
			req.Hidden = []string{}
		}
		val, _ := json.Marshal(req.Hidden)
		if err := h.repo.SetSystemSetting(r.Context(), settingUserHiddenRuleTemplates, string(val)); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *RuleTemplatesHandler) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	templatesDir := "rule_templates"

	// 读取目录
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		http.Error(w, "Failed to read templates directory", http.StatusInternalServerError)
		return
	}

	// 过滤模板文件(.yaml/.yml Clash + .conf Surge)
	var templates []string
	for _, entry := range entries {
		if !entry.IsDir() && isRuleTemplateFile(entry.Name()) {
			templates = append(templates, entry.Name())
		}
	}

	// 归属信息(供前端控制"删除/修改仅自己的"),并附带当前用户名与是否管理员。
	allOwners, _ := h.repo.ListRuleTemplateOwners(r.Context())
	username := auth.UsernameFromContext(r.Context())
	isAdmin := userIsAdmin(r.Context(), h.repo, username)

	// 数据隔离:
	//   - admin → 全部归属信息原样返回(管理需要)
	//   - 非 admin → templates 里隐藏"别人私有的模板文件",owners 只保留自己的归属信息
	//     (防止普通用户从该接口枚举其它用户名)
	visibleTemplates := templates
	visibleOwners := allOwners
	if !isAdmin {
		hidden := h.loadHiddenRuleTemplates(r)
		visibleOwners = make(map[string]string, 1)
		filtered := make([]string, 0, len(templates))
		for _, fn := range templates {
			owner, hasOwner := allOwners[fn]
			if !hasOwner {
				// 无归属记录 = 内置/公共模板:默认所有人可见,但管理员可在「模板可见性」里隐藏。
				if !hidden[fn] {
					filtered = append(filtered, fn)
				}
				continue
			}
			if owner == username {
				// 自己的私有模板:保留 + 暴露归属(给前端显示"可编辑/删除")
				filtered = append(filtered, fn)
				visibleOwners[fn] = owner
			}
			// 其它人的私有模板:对当前用户彻底隐藏
		}
		visibleTemplates = filtered
	}

	// 返回 JSON 响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"templates": visibleTemplates,
		"owners":    visibleOwners,
		"username":  username,
		"is_admin":  isAdmin,
	})
}

func (h *RuleTemplatesHandler) handleGetTemplate(w http.ResponseWriter, r *http.Request, templateName string) {
	// 安全性：防止目录遍历
	if strings.Contains(templateName, "..") || strings.Contains(templateName, "/") || strings.Contains(templateName, "\\") {
		http.Error(w, "Invalid template name", http.StatusBadRequest)
		return
	}

	templatesDir := "rule_templates"
	templatePath := filepath.Join(templatesDir, templateName)

	// 检查文件是否存在
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}

	// 读取文件内容
	content, err := os.ReadFile(templatePath)
	if err != nil {
		http.Error(w, "Failed to read template", http.StatusInternalServerError)
		return
	}

	// 返回包含内容的 JSON 响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"content": string(content),
	})
}

func (h *RuleTemplatesHandler) handleUpdateTemplate(w http.ResponseWriter, r *http.Request, templateName string) {
	// 安全性：防止目录遍历
	if strings.Contains(templateName, "..") || strings.Contains(templateName, "/") || strings.Contains(templateName, "\\") {
		http.Error(w, "Invalid template name", http.StatusBadRequest)
		return
	}

	templatesDir := "rule_templates"
	templatePath := filepath.Join(templatesDir, templateName)

	// 检查文件是否存在
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "模板文件不存在",
		})
		return
	}

	// 解析请求体
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 单文件大小上限
	if len(payload.Content) > ruleTemplateMaxFileSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("模板内容过大,不能超过 %dMB", ruleTemplateMaxFileSize>>20),
		})
		return
	}

	// 将内容写入文件
	if err := os.WriteFile(templatePath, []byte(payload.Content), 0644); err != nil {
		http.Error(w, "Failed to save template", http.StatusInternalServerError)
		return
	}

	// 返回成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "模板保存成功",
	})
}

func (h *RuleTemplatesHandler) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, templateName string) {
	// 安全性：防止目录遍历
	if strings.Contains(templateName, "..") || strings.Contains(templateName, "/") || strings.Contains(templateName, "\\") {
		http.Error(w, "Invalid template name", http.StatusBadRequest)
		return
	}

	templatesDir := "rule_templates"
	templatePath := filepath.Join(templatesDir, templateName)

	// 检查文件是否存在
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "模板文件不存在",
		})
		return
	}

	// 删除文件
	if err := os.Remove(templatePath); err != nil {
		http.Error(w, "Failed to delete template", http.StatusInternalServerError)
		return
	}
	_ = h.repo.DeleteRuleTemplateOwner(r.Context(), templateName)

	// 返回成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "模板删除成功",
	})
}

func (h *RuleTemplatesHandler) handleRenameTemplate(w http.ResponseWriter, r *http.Request) {
	// 解析请求体
	var payload struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	oldName := strings.TrimSpace(payload.OldName)
	newName := strings.TrimSpace(payload.NewName)

	// 验证姓名
	if oldName == "" || newName == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "文件名不能为空",
		})
		return
	}

	// 安全性：防止目录遍历
	if strings.Contains(oldName, "..") || strings.Contains(oldName, "/") || strings.Contains(oldName, "\\") ||
		strings.Contains(newName, "..") || strings.Contains(newName, "/") || strings.Contains(newName, "\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	// 确保新名称保留合法扩展名;缺失时按原文件类型补全(.conf 保持 Surge,其余默认 .yaml)
	if !isRuleTemplateFile(newName) {
		if strings.HasSuffix(oldName, ".conf") {
			newName = newName + ".conf"
		} else {
			newName = newName + ".yaml"
		}
	}

	// 归属校验:仅管理员或模板所有者可重命名
	if !h.canModifyRuleTemplate(r, oldName) {
		http.Error(w, "无权重命名该模板", http.StatusForbidden)
		return
	}

	templatesDir := "rule_templates"
	oldPath := filepath.Join(templatesDir, oldName)
	newPath := filepath.Join(templatesDir, newName)

	// 检查旧文件是否存在
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "原文件不存在",
		})
		return
	}

	// 检查新文件是否已存在
	if _, err := os.Stat(newPath); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "目标文件名已存在",
		})
		return
	}

	// 重命名文件
	if err := os.Rename(oldPath, newPath); err != nil {
		http.Error(w, "Failed to rename template", http.StatusInternalServerError)
		return
	}
	_ = h.repo.RenameRuleTemplateOwner(r.Context(), oldName, newName)

	// 返回成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":  "模板重命名成功",
		"filename": newName,
	})
}

func (h *RuleTemplatesHandler) handleUploadTemplate(w http.ResponseWriter, r *http.Request) {
	// 解析多部分表单（限制为 10MB）
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	// 从表单中获取文件
	file, header, err := r.FormFile("template")
	if err != nil {
		http.Error(w, "Failed to get file from request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 验证文件扩展名(.yaml/.yml Clash + .conf Surge)
	filename := header.Filename
	if !isRuleTemplateFile(filename) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "只支持 .yaml、.yml 或 .conf 文件",
		})
		return
	}

	// 单文件大小上限
	if header.Size > ruleTemplateMaxFileSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("模板文件过大,单文件不能超过 %dMB", ruleTemplateMaxFileSize>>20),
		})
		return
	}

	// 安全性：清理文件名
	filename = filepath.Base(filename)
	if strings.Contains(filename, "..") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	// 如果不存在则创建模板目录
	templatesDir := "rule_templates"
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		http.Error(w, "Failed to create templates directory", http.StatusInternalServerError)
		return
	}

	// per-user 配额:普通用户上传规则模板受管理员配的「模板数量限制」约束(admin 不限)。
	// 原先此路径只有下面的全局上限,不查 per-user 配额 —— 导致「模板管理」的数量限制形同虚设。
	if err := checkUserQuota(r.Context(), h.repo, auth.UsernameFromContext(r.Context()), "template"); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}

	// 数量上限(全局)
	if countRuleTemplates(templatesDir) >= ruleTemplateMaxCount {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("模板数量已达上限 (%d)", ruleTemplateMaxCount),
		})
		return
	}

	// 创建目标文件
	templatePath := filepath.Join(templatesDir, filename)

	// 检查文件是否已经存在
	if _, err := os.Stat(templatePath); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("模板文件 %s 已存在", filename),
		})
		return
	}

	dst, err := os.Create(templatePath)
	if err != nil {
		http.Error(w, "Failed to create template file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// 复制文件内容(限制大小,防止 multipart 头部声明不实)
	written, err := io.Copy(dst, io.LimitReader(file, ruleTemplateMaxFileSize+1))
	if err != nil {
		os.Remove(templatePath)
		http.Error(w, "Failed to save template file", http.StatusInternalServerError)
		return
	}
	if written > ruleTemplateMaxFileSize {
		os.Remove(templatePath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("模板文件过大,单文件不能超过 %dMB", ruleTemplateMaxFileSize>>20),
		})
		return
	}

	// 记录归属(普通用户上传 → 该用户;管理员上传 → 管理员用户名)
	_ = h.repo.SetRuleTemplateOwner(r.Context(), filename, auth.UsernameFromContext(r.Context()))

	// 返回带有文件名的成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"filename": filename,
		"message":  "模板上传成功",
	})
}
