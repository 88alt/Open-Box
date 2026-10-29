package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// KernelConn 是 Clash API `GET /connections` 里一条连接中和流量统计有关的部分。
type KernelConn struct {
	ID       string
	Upload   int64    // 这条连接累计发往外网的字节
	Download int64    // 这条连接累计收到的字节
	Chains   []string // 出站链:第一个是实际出口(节点或直连),最后一个是路由选中的组(官方 Clash API 的约定)
	Source   string   // 终端的来源 IP
	Host     string   // 访问目标的域名(嗅探 / 解析出来的),没有时为空
	DestIP   string   // 访问目标的 IP,没有时为空
	// SourcePort 是来源端口(十进制字符串),没有时为空。站点延迟靠「来源 IP + 端口」认出自己发的那条连接。
	SourcePort string
	// Rule / RulePayload 是内核说这条连接命中了哪条路由规则(Clash API 的 rule / rulePayload,例如
	// 规则类型和它的内容);没有时为空。route-test 靠它告诉用户是哪条规则管的。
	Rule        string
	RulePayload string
}

// KernelSnapshot 是某一刻的连接快照。UploadTotal / DownloadTotal 是内核自启动以来的累计总量
// (含已经关闭的短连接),内核重启会归零;DirectTags 是当前配置里所有 direct 类型出站的名字。
type KernelSnapshot struct {
	UploadTotal   int64
	DownloadTotal int64
	Conns         []KernelConn
	DirectTags    []string
}

// KernelConnections 读正在运行的 sing-box 的连接快照(只读,GET /connections,sing-box 官方 Clash API)。
// 控制地址和 secret 取自正式配置;内核没在跑、没开控制接口、鉴权失败都返回 error。
func KernelConnections(ctx context.Context, configFile string) (KernelSnapshot, error) {
	b, err := readLimited(configFile, 16<<20)
	if err != nil {
		return KernelSnapshot{}, fmt.Errorf("读不到当前配置: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return KernelSnapshot{}, errors.New("当前配置不是合法 JSON")
	}
	addr, secret := controllerOf(cfg)
	if addr == "" {
		return KernelSnapshot{}, errors.New("配置里没有开启控制接口")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	u := url.URL{Scheme: "http", Host: addr, Path: "/connections"}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return KernelSnapshot{}, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return KernelSnapshot{}, errors.New("控制接口连不上(内核没在运行?)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return KernelSnapshot{}, fmt.Errorf("控制接口返回 %d", resp.StatusCode)
	}
	snap, err := parseKernelConnections(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return KernelSnapshot{}, err
	}
	snap.DirectTags = directTagsOf(cfg)
	return snap, nil
}

func parseKernelConnections(r io.Reader) (KernelSnapshot, error) {
	var doc struct {
		UploadTotal   int64 `json:"uploadTotal"`
		DownloadTotal int64 `json:"downloadTotal"`
		Connections   []struct {
			ID          string   `json:"id"`
			Upload      int64    `json:"upload"`
			Download    int64    `json:"download"`
			Chains      []string `json:"chains"`
			Rule        string   `json:"rule"`
			RulePayload string   `json:"rulePayload"`
			Metadata    struct {
				SourceIP      string `json:"sourceIP"`
				SourcePort    any    `json:"sourcePort"`
				Host          string `json:"host"`
				DestinationIP string `json:"destinationIP"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return KernelSnapshot{}, errors.New("控制接口返回的连接列表不是合法 JSON")
	}
	snap := KernelSnapshot{UploadTotal: doc.UploadTotal, DownloadTotal: doc.DownloadTotal}
	for _, c := range doc.Connections {
		if c.ID == "" {
			continue
		}
		snap.Conns = append(snap.Conns, KernelConn{
			ID: c.ID, Upload: c.Upload, Download: c.Download, Chains: c.Chains,
			Source: c.Metadata.SourceIP, Host: c.Metadata.Host, DestIP: c.Metadata.DestinationIP,
			SourcePort: portString(c.Metadata.SourcePort),
			Rule:       c.Rule, RulePayload: c.RulePayload,
		})
	}
	return snap, nil
}

// directTagsOf 取配置里所有 direct 类型出站的名字。
func directTagsOf(cfg map[string]any) []string {
	var tags []string
	list, _ := cfg["outbounds"].([]any)
	for _, o := range list {
		m, _ := o.(map[string]any)
		if t, _ := m["type"].(string); t == "direct" {
			if tag, _ := m["tag"].(string); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

// portString 把 JSON 里的端口(Clash API 给的是字符串,也容忍数字)转成十进制字符串。
func portString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) && x > 0 {
			return strconv.FormatInt(int64(x), 10)
		}
	}
	return ""
}
