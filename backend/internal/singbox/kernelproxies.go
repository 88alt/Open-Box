package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// KernelPoint 是内核记的一次测速:Time 是 RFC3339 时间,Delay 是毫秒。
type KernelPoint struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

// KernelProxy 是 Clash API `GET /proxies` 里一个代理的、和延迟历史有关的部分。
// 节点组的 Now 是它当前选中的节点名;内核每个代理只留最新一次(测失败时记录被删掉)。
type KernelProxy struct {
	Name    string
	Type    string
	Now     string
	History []KernelPoint
}

// KernelProxies 读正在运行的 sing-box 的代理列表和各自的测速记录(只读,GET /proxies,sing-box 官方 Clash API)。
// 控制地址和 secret 取自正式配置;内核没在跑、没开控制接口、鉴权失败都返回 error。
func KernelProxies(ctx context.Context, configFile string) ([]KernelProxy, error) {
	b, err := readLimited(configFile, 16<<20)
	if err != nil {
		return nil, fmt.Errorf("读不到当前配置: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, errors.New("当前配置不是合法 JSON")
	}
	addr, secret := controllerOf(cfg)
	if addr == "" {
		return nil, errors.New("配置里没有开启控制接口")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u := url.URL{Scheme: "http", Host: addr, Path: "/proxies"}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return nil, errors.New("控制接口连不上(内核没在运行?)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("控制接口返回 %d", resp.StatusCode)
	}
	return parseKernelProxies(io.LimitReader(resp.Body, 32<<20))
}

func parseKernelProxies(r io.Reader) ([]KernelProxy, error) {
	var doc struct {
		Proxies map[string]struct {
			Type    string        `json:"type"`
			Now     string        `json:"now"`
			History []KernelPoint `json:"history"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, errors.New("控制接口的返回格式不对")
	}
	out := make([]KernelProxy, 0, len(doc.Proxies))
	for name, p := range doc.Proxies {
		out = append(out, KernelProxy{Name: name, Type: p.Type, Now: p.Now, History: p.History})
	}
	return out, nil
}
