package latency

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// kernelProbesTimeout 是问内核测速排队情况的时限;对齐上游:3 秒问不到就当不忙(变量只为测试里缩短)。
var kernelProbesTimeout = 3 * time.Second

// QueueRes 是 GET /api/openbox/nodes/latency/queue 的响应。
type QueueRes struct {
	// Busy:内核前面有测速在排队,或在跑的数量已到上限;界面据此在右上角提示「已排队」
	Busy bool `json:"busy"`
	// Running:内核正在跑的测速数
	Running float64 `json:"running"`
	// Waiting:内核里排队等着的测速数(交互式 + 后台)
	Waiting float64 `json:"waiting"`
}

// num 把内核回的数取成数字:数字原样,数字字符串转数字,其它(含缺省、负无穷之类读不出的)记 0,
// 对应上游的 Number(x) || 0。
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil && f == f {
			return f
		}
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// queueState 向内核的 GET /openbox/probes 问测速排队情况(对齐上游 v0.1.423 的 nodes/latency/queue)。
// 内核没在跑、连不上、控制接口读不到、老内核没有这个接口(非 2xx)、回的不是 JSON 对象,一律当不忙。
func (h *handlers) queueState(ctx context.Context) QueueRes {
	if h.Kernel == nil {
		return QueueRes{}
	}
	t, err := h.Kernel()
	if err != nil || t.Addr == "" {
		return QueueRes{}
	}
	ctx, cancel := context.WithTimeout(ctx, kernelProbesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+t.Addr+"/openbox/probes", nil)
	if err != nil {
		return QueueRes{}
	}
	if t.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+t.Secret)
	}
	client := h.KernelClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return QueueRes{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return QueueRes{}
	}
	var kernel map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&kernel); err != nil || kernel == nil {
		return QueueRes{}
	}
	running := num(kernel["running"])
	waiting := num(kernel["interactive"]) + num(kernel["background"])
	limit := num(kernel["limit"])
	return QueueRes{
		Busy:    waiting > 0 || (limit > 0 && running >= limit),
		Running: running,
		Waiting: waiting,
	}
}

// queue 处理 GET /api/openbox/nodes/latency/queue:手动测速开始前看一眼内核的测速排队。
func (h *handlers) queue(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, h.queueState(r.Context()))
}
