// Package dns 实现 /api/openbox/dns/* 里与「上游 DNS」有关的接口:
// GET dns/wan、GET / POST dns/region-detect、POST dns/flush-cache、POST dns/upstream-test。
package dns

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/dnscache"
	"github.com/timeflysoon/Open-Box/backend/internal/dnstest"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
)

// Detector 按出口公网 IP 判地区(*regiondetect.Detector 实现它),测试里换假的。
type Detector interface {
	Pending() bool
	Detect(ctx context.Context) regiondetect.Result
}

// Deps 是本分组的依赖。
type Deps struct {
	// WAN 返回系统此刻的上游 DNS(*system.ReadWANDNS 的包装),测试里换假的。为空或返回 nil 都按「没有」处理。
	WAN func() []string
	// Region 返回档案里此刻的 DNS 地区("cn" / "intl");为空或返回别的值都按 "cn"(与前端一致)。
	Region func(ctx context.Context) string
	// Detector 为空时 region-detect 返回 503。
	Detector Detector
	// Flush 清三层 DNS 缓存(*dnscache.Layers.Flush);为空时 flush-cache 返回 503。
	Flush func(ctx context.Context) dnscache.Result
	// Test 让内核按所选协议向一台 DNS 上游真查一次(*dnstest.Tester.Test);为空时 upstream-test 返回 503。
	Test func(ctx context.Context, req dnstest.Request) (dnstest.Result, error)
}

const maxTestBody = 4 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/dns/wan", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.wan,
	}))
	mux.HandleFunc("/api/openbox/dns/flush-cache", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.flush,
	}))
	mux.HandleFunc("/api/openbox/dns/upstream-test", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.upstreamTest,
	}))
	mux.HandleFunc("/api/openbox/dns/region-detect", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet:  d.regionState,
		http.MethodPost: d.regionDetect,
	}))
}

// regionState 对应前端 fetchDnsRegionState:{ pending, region }。
// pending 只在有一次判断正在进行时为真;后端启动时不会自己判、更不会自己改档案(见 STATUS.md 日志)。
func (d Deps) regionState(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	region := "cn"
	if d.Region != nil && d.Region(r.Context()) == "intl" {
		region = "intl"
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"pending": d.Detector != nil && d.Detector.Pending(),
		"region":  region,
	})
}

// regionDetect 对应前端 detectDnsRegion:只查、只回报,不写档案(前端拿到结果后自己走 saveProfile)。
// 判不出来仍是 200,region 为 null、error 写原因;前端按「判不出来就按当前地区」处理。
func (d Deps) regionDetect(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Detector == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "地区判断不可用")
		return
	}
	httpx.JSON(w, http.StatusOK, d.Detector.Detect(r.Context()))
}

// wan 对应前端 fetchWanDns:{ servers: string[] }。读不到时回空数组而不是报错——
// 前端拿它填「上游 DNS」那一项的说明,没有就不显示。
func (d Deps) wan(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	servers := []string{}
	if d.WAN != nil {
		if s := d.WAN(); s != nil {
			servers = s
		}
	}
	httpx.JSON(w, http.StatusOK, map[string][]string{"servers": servers})
}

// flush 对应前端 flushDnsCache:内核、dnsmasq、systemd-resolved 各清一层,有一层清掉就算成功;
// 全都清不掉回 503(前端会把 error 文字显示出来)。
func (d Deps) flush(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var res dnscache.Result
	if d.Flush != nil {
		res = d.Flush(r.Context())
	}
	if !res.Any() {
		httpx.Error(w, http.StatusServiceUnavailable, "没有清掉任何一层 DNS 缓存:内核没在运行(或没开控制接口),也没有找到在运行的 dnsmasq 或 systemd-resolved")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// upstreamTest 对应前端 testDnsUpstream。请求不合法回 400;现在测不了(没有 sing-box、核心不可用)回 503;
// 测了但没通是 200 加 ok:false 和原因(前端按 ok 判断,并把 error 显示出来)。
func (d Deps) upstreamTest(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Test == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "DNS 上游测试不可用")
		return
	}
	var req dnstest.Request
	if !httpx.DecodeJSON(w, r, &req, maxTestBody) {
		return
	}
	res, err := d.Test(r.Context(), req)
	var ie *dnstest.InputError
	var ue *dnstest.UnavailableError
	switch {
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case errors.As(err, &ue):
		httpx.Error(w, http.StatusServiceUnavailable, ue.Msg)
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "DNS 上游测试失败: "+err.Error())
	default:
		httpx.JSON(w, http.StatusOK, res)
	}
}
