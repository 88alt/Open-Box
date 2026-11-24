package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 面板里「各节点组此刻选中哪个节点」的来源与存法(双内核 3e-5 的 selections 入参)。
//
// 上游 v0.1.462 的 Node 服务端是这么取的(`St` / `Pr` / `io`,见 panel/server/index.mjs):
//  1. 优先问正在运行的 Mihomo 要(`GET /proxies` 里每个组件的 `now`),这是用户在面板 / 客户端里改过的实时选择;
//  2. 实时读不到(内核没在跑、不是 Mihomo、鉴权失败)就用上次切换成功后留下的快照;
//  3. 两者都没有就是空——装配时按组里的默认第一项走。
//
// 这里只做读与存;把选中项套到组上由 ApplySelections 做,写快照由切换流程成功后调 SelectionsStore.Set。

// SelectionsKey 是选中项快照在存储里的名字(与其它面板数据一样落成一个 JSON 文件)。
const SelectionsKey = "selections"

// MihomoControllerAddr 是装配时写进 mihomo.yaml 的控制接口地址(3e-5 的 `external-controller`)。
const MihomoControllerAddr = "127.0.0.1:9095"

// SelectionsStorage 是快照的存储(backend 的 *store.Store 满足)。
type SelectionsStorage interface {
	Load(name string, v any) (bool, error)
	Save(name string, v any) error
}

// SelectionsStore 存取选中项快照({组名: 节点名})。
type SelectionsStore struct{ Store SelectionsStorage }

// Get 读快照。没有、读不出来、内容不是对象都返回空 map(不报错:选中项坏了不该挡住装配)。
func (s SelectionsStore) Get() map[string]any {
	out := map[string]any{}
	if s.Store == nil {
		return out
	}
	var raw map[string]any
	ok, err := s.Store.Load(SelectionsKey, &raw)
	if err != nil || !ok {
		return out
	}
	for k, v := range raw {
		if name, ok := v.(string); ok && k != "" && name != "" {
			out[k] = name
		}
	}
	return out
}

// Set 写快照。只留字符串值(与 Get 一致);空的选择也照样写,表示「现在没有选中项」。
func (s SelectionsStore) Set(sel map[string]any) error {
	if s.Store == nil {
		return errors.New("没有选中项存储")
	}
	out := make(map[string]any, len(sel))
	for k, v := range sel {
		if name, ok := v.(string); ok && k != "" && name != "" {
			out[k] = name
		}
	}
	return s.Store.Save(SelectionsKey, out)
}

// ControllerSelections 问正在运行的 Mihomo 要各组此刻的选择(GET /proxies,取每个组件的 now)。
// 控制接口连不上、返回格式不对都返回 error,由调用方决定退到快照。
func ControllerSelections(ctx context.Context, addr, secret string) (map[string]string, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, errors.New("没有控制接口地址")
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
	return ParseControllerSelections(io.LimitReader(resp.Body, 32<<20))
}

// ParseControllerSelections 从 `GET /proxies` 的返回里取出「组名 → now」(只留 now 非空的)。
func ParseControllerSelections(r io.Reader) (map[string]string, error) {
	var doc struct {
		Proxies map[string]struct {
			Type string `json:"type"`
			Now  string `json:"now"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, errors.New("控制接口的返回格式不对")
	}
	out := map[string]string{}
	for name, p := range doc.Proxies {
		if name != "" && p.Now != "" {
			out[name] = p.Now
		}
	}
	return out, nil
}

// SelectionsToTags 对应上游 `Pr`:Mihomo 的实时选择里,内置出站写的是 DIRECT / REJECT,
// 换成我们自己配置里的内置组名字(ApplySelections 与路由计划认的是后者)。
func SelectionsToTags(now map[string]string, b BuiltinNames) map[string]any {
	b = b.withDefaults()
	out := make(map[string]any, len(now))
	for group, pick := range now {
		switch pick {
		case "DIRECT":
			pick = b.Direct
		case "REJECT":
			pick = b.Block
		}
		if group != "" && pick != "" {
			out[group] = pick
		}
	}
	return out
}
