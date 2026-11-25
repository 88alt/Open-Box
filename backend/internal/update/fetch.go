package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// maxSums 是 SHA256SUMS 的读取上限(几十行,64 KiB 足够)。
const maxSums = 64 << 10

// defaultDownloadClient 是下载用的客户端:**不设整体超时**(一个 100 MB 的包在慢线路上要几分钟),
// 只限制建连与等响应头的时间;卡住不动的连接靠 ctx 取消或用户点取消来收场。
func defaultDownloadClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// assetURL 把资产地址套上通道:直连用原地址,镜像则前面加前缀(M6 第八节)。
func assetURL(a Asset, via string) string {
	if via == "" || via == "api" {
		return a.URL
	}
	return via + a.URL
}

// open 发起一次 GET 并把响应体交给 fn。非 200 直接失败;Content-Length 未知时 total 为 0。
func open(ctx context.Context, client *http.Client, url string, fn func(r io.Reader, total int64) error) error {
	if client == nil {
		client = defaultDownloadClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("下载地址不对")
	}
	req.Header.Set("User-Agent", "openbox-backend")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return errors.New("下载失败:连不上服务器")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败:服务器回了状态码 %d", resp.StatusCode)
	}
	return fn(resp.Body, resp.ContentLength)
}

// fetchSums 取 SHA256SUMS 的内容。它和安装包走同一个通道(镜像若恶意可以同时改两个,v1 接受这个限制)。
func fetchSums(ctx context.Context, client *http.Client, a Asset, via string) ([]byte, error) {
	var out []byte
	err := open(ctx, client, assetURL(a, via), func(r io.Reader, _ int64) error {
		b, err := io.ReadAll(io.LimitReader(r, maxSums+1))
		if err != nil {
			return errors.New("读取 SHA256SUMS 失败")
		}
		if len(b) > maxSums {
			return errors.New("SHA256SUMS 大得不像话,拒绝使用")
		}
		out = b
		return nil
	})
	return out, err
}
