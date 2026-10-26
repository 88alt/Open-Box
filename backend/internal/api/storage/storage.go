// Package storage 实现前端的两个「持久化小仓库」:
//   - /api/storage:把浏览器 localStorage 里 config/ 开头的设置同步到后端,换浏览器也能保留;
//   - /api/background-image:自定义背景图(data URL)。
package storage

import (
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	settingsName = "web-settings"
	imageName    = "background-image"

	managedPrefix = "config/"
	// 前端早期把登录状态也存进过 localStorage,现在会主动剔除;后端同样丢弃,不落盘。
	legacyAuthKey = "config/access-password-authenticated"

	maxSettingsBody = 4 << 20 // 4 MiB
	maxEntries      = 2000
	maxKeyBytes     = 256
	maxImageBody    = 12 << 20 // 12 MiB(data URL 含 base64 膨胀)
)

// Deps 是本分组的依赖。
type Deps struct{ Store *store.Store }

type handlers struct{ st *store.Store }

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{st: d.Store}
	mux.HandleFunc("/api/storage", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.getSettings,
		http.MethodPut: h.putSettings,
	}))
	mux.HandleFunc("/api/background-image", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet:    h.getImage,
		http.MethodPut:    h.putImage,
		http.MethodDelete: h.deleteImage,
	}))
}

type settingsFile struct {
	Entries map[string]string `json:"entries"`
}

func (h *handlers) getSettings(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	f := settingsFile{}
	if _, err := h.st.Load(settingsName, &f); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取设置失败: "+err.Error())
		return
	}
	if f.Entries == nil {
		f.Entries = map[string]string{}
	}
	httpx.JSON(w, http.StatusOK, f)
}

// putSettings 用请求里的快照整体替换已保存的设置(前端每次发送完整快照)。
// 不合规的条目(前缀不对、值不是字符串、键过长、遗留的登录状态键)直接丢弃而不是整体报错,
// 与前端 normalizeManagedStorageSnapshot 的做法一致。
func (h *handlers) putSettings(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body struct {
		Entries map[string]any `json:"entries"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxSettingsBody) {
		return
	}
	if len(body.Entries) > maxEntries {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "设置条目过多")
		return
	}
	clean := make(map[string]string, len(body.Entries))
	for k, v := range body.Entries {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(k, managedPrefix) || len(k) > maxKeyBytes || k == legacyAuthKey {
			continue
		}
		clean[k] = s
	}
	if err := h.st.Save(settingsName, settingsFile{Entries: clean}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(clean)})
}

type imageFile struct {
	Image string `json:"image"`
}

func (h *handlers) getImage(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	f := imageFile{}
	if _, err := h.st.Load(imageName, &f); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取背景图失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

func (h *handlers) putImage(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body imageFile
	if !httpx.DecodeJSON(w, r, &body, maxImageBody) {
		return
	}
	if !strings.HasPrefix(body.Image, "data:image/") {
		httpx.Error(w, http.StatusBadRequest, "背景图必须是 data:image/ 开头的图片数据")
		return
	}
	if err := h.st.Save(imageName, body); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "保存背景图失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handlers) deleteImage(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	if err := h.st.Delete(imageName); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "删除背景图失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
