// Package configpreview 实现 /api/openbox/config/preview:按当前已保存的档案、订阅、节点组现算一份配置。
// 不落盘、不校验、不动系统,前端用它列出只读的策略组;配置里的密钥会被遮掉。
package configpreview

import (
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// Deps 是本分组的依赖。
type Deps struct{ Gen *singbox.Generator }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/config/preview", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			httpx.NoStore(w)
			b, err := d.Gen.Build(r.Context())
			if err != nil {
				var ce *core.CoreError
				switch {
				case errors.Is(err, singbox.ErrCoreUnavailable):
					httpx.Error(w, http.StatusServiceUnavailable, err.Error())
				case errors.As(err, &ce):
					// 核心的错误信息本来就是给人看的(如「出站名称重复」)
					httpx.Error(w, http.StatusBadRequest, ce.Message)
				default:
					httpx.Error(w, http.StatusInternalServerError, "生成配置失败: "+err.Error())
				}
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"config": singbox.Redacted(b.Config)})
		},
	}))
}
