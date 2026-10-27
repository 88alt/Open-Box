package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// KernelFlushDNS 清空正在运行的 sing-box 的 DNS 缓存:读正式配置里 clash_api 的控制地址和 secret,
// 调 POST /cache/dns/flush(sing-box 官方 Clash API 的一部分,成功回 204)。
//
// 只读配置、只发这一个请求,不碰进程和配置。内核没在跑、没开控制接口、或鉴权失败都返回 error,
// 由调用方当作「这一层没清掉」处理。
func KernelFlushDNS(ctx context.Context, configFile string) error {
	b, err := readLimited(configFile, 16<<20)
	if err != nil {
		return fmt.Errorf("读不到当前配置: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return errors.New("当前配置不是合法 JSON")
	}
	addr, secret := controllerOf(cfg)
	if addr == "" {
		return errors.New("配置里没有开启控制接口")
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := url.URL{Scheme: "http", Host: addr, Path: "/cache/dns/flush"}
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return errors.New("控制接口连不上(内核没在运行?)")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("控制接口返回 %d", resp.StatusCode)
	}
	return nil
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("文件过大")
	}
	return b, nil
}
