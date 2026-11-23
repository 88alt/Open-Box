package subscriptions

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// nodeShare 处理 POST /api/openbox/subscriptions/node-share(订阅预览里单个节点的「分享」)。
//
// 请求:订阅预览同一套来源与改名规则(url / urls / urlEntries / content / loginId / userAgent / name / renameOptions,
// 可带 directSubscription),再加
//
//	originalTag  节点在订阅里的原名
//	occurrence   同名节点里的第几个(从 0 数;订阅里有重名节点时用它区分,不传 = 0)
//	newTag       分享链接里要用的节点名(面板里正显示的名字);空 = 用规则算出来的名字
//
// 响应:{ name, link }。节点由服务端按来源重新解析后取出,含密码的配置不经浏览器往返。
func (h *handlers) nodeShare(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, ok := decode(w, r)
	if !ok {
		return
	}
	in, src, ok := parse(w, raw)
	if !ok {
		return
	}
	orig, ok := str(raw, "originalTag")
	if !ok || orig == nil || strings.TrimSpace(*orig) == "" {
		httpx.Error(w, http.StatusBadRequest, "originalTag 必须是非空字符串")
		return
	}
	newTag, ok := str(raw, "newTag")
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "newTag 必须是字符串")
		return
	}
	occurrence := 0
	if v, has := raw["occurrence"]; has && v != nil {
		f, isNum := v.(float64)
		if !isNum || f < 0 || f != math.Trunc(f) || f > 1e6 {
			httpx.Error(w, http.StatusBadRequest, "occurrence 必须是非负整数")
			return
		}
		occurrence = int(f)
	}
	if in.Source == nil {
		httpx.Error(w, http.StatusBadRequest, "缺少订阅来源")
		return
	}
	name := ""
	if in.Name != nil {
		name = *in.Name
	}

	nodes, err := h.s.PreviewNodes(r.Context(), src, name, in.Rename)
	if err != nil {
		h.fail(w, err, "解析订阅失败")
		return
	}
	var node *sub.Node
	seen := 0
	for i := range nodes {
		o := nodes[i].OriginalTag
		if o == "" {
			o = nodes[i].Tag
		}
		if o != *orig {
			continue
		}
		if seen == occurrence {
			node = &nodes[i]
			break
		}
		seen++
	}
	if node == nil {
		httpx.Error(w, http.StatusNotFound, "订阅里找不到这个节点(可能已被过滤、停用或订阅已更新)")
		return
	}

	label := node.Tag
	if newTag != nil && strings.TrimSpace(*newTag) != "" {
		label = strings.TrimSpace(*newTag)
	}
	if strings.TrimSpace(label) == "" {
		label = *orig
	}
	node.Tag = label
	link, err := sub.EncodeLink(*node)
	if err != nil {
		var nl *sub.ErrNoShareLink
		if errors.As(err, &nl) {
			httpx.Error(w, http.StatusBadRequest, nl.Reason)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "生成分享链接失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"name": label, "link": link})
}
