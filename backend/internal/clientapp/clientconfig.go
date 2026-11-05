package clientapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// 本地分流(客户端配置)的同步状态:同步开关、配对码、nonce、最后同步时刻。
// 本文件只管「状态」:读、开关、更换配对码。配置怎么用配对码封起来、App 用的公开同步接口,
// 前端没有规定,见 STATUS.md「client-config/file」条目,这里不涉及。对齐上游 v0.1.402 配对码长度与字母表(维护者授权参考上游)。
//
// 配对码属于凭据:只回给已登录的面板,不写日志,响应带 no-store。

const (
	// pairCodeAlphabet 对齐上游 v0.1.402:A-Z 与 0-9 共 36 个字符。
	pairCodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	pairCodeLen      = 4  // 上游 cS:4 个字符
	nonceBytes       = 12 // 上游 nP:12 字节 → 24 位十六进制
	maxRotateTries   = 16 // 换码时最多重抽几次,保证新旧不同(随机源坏了才会用尽)
)

type configState struct {
	Enabled    bool   `json:"enabled"`
	PairCode   string `json:"pairCode"`
	Nonce      string `json:"nonce"`
	LastSyncAt int64  `json:"lastSyncAt,omitempty"` // 毫秒;从没同步过是 0
}

// Issue 是 GET /api/openbox/client-config 的响应,字段与前端 OpenboxClientConfigIssue 一一对应。
type Issue struct {
	Enabled      bool     `json:"enabled"`
	PairCode     string   `json:"pairCode"`
	Nonce        string   `json:"nonce"`
	LastSyncAt   int64    `json:"lastSyncAt"`
	RouterID     string   `json:"routerId"`
	RouterName   string   `json:"routerName"`
	LanAddresses []string `json:"lanAddresses"`
	PanelPort    int      `json:"panelPort"`
}

// ClientConfig 返回当前状态。第一次读时生成配对码和 nonce 并保存,同步开关默认关着
// (开着就等于对局域网和外网放出 App 用的公开接口,要用户自己点开)。
func (s *Service) ClientConfig(ctx context.Context) (Issue, error) {
	return s.configChange(ctx, nil)
}

// SetClientConfigEnabled 打开或关闭同步开关,返回新值。不动配对码和最后同步时刻。
func (s *Service) SetClientConfigEnabled(ctx context.Context, enabled bool) (bool, error) {
	is, err := s.configChange(ctx, func(c *configState) error { c.Enabled = enabled; return nil })
	return is.Enabled, err
}

// RotateClientConfig 更换配对码和 nonce(之前导入的设备、导出的文件随之失效),
// 最后同步时刻清零(那是旧配对的记录),开关保持原样。返回换完的整份。
func (s *Service) RotateClientConfig(ctx context.Context) (Issue, error) {
	is, err := s.configChange(ctx, func(c *configState) error {
		old := c.PairCode
		code := old
		for i := 0; i < maxRotateTries && code == old; i++ {
			var err error
			if code, err = newPairCode(s.Rand); err != nil {
				return err
			}
		}
		if code == old {
			return errors.New("没能抽出和旧配对码不同的新码")
		}
		n, err := newNonce(s.Rand)
		if err != nil {
			return err
		}
		c.PairCode, c.Nonce, c.LastSyncAt = code, n, 0
		return nil
	})
	if err != nil {
		return is, err
	}
	var st state
	_ = s.Store.Update(stateFile, &st, func() error {
		clearDevices(&st)
		return nil
	})
	return is, nil
}

// configChange 先把路由器标识等不会变的部分读出来(读不到就什么都不改),再在一次 store.Update 里
// 补齐缺的 / 坏的配对码和 nonce,然后应用 mutate。mutate 为 nil 就是只读(必要时补齐)。
func (s *Service) configChange(ctx context.Context, mutate func(*configState) error) (Issue, error) {
	info, err := s.Info(ctx)
	if err != nil {
		return Issue{}, err
	}
	var st state
	err = s.Store.Update(stateFile, &st, func() error {
		if st.Config == nil {
			st.Config = &configState{}
		}
		changed := false
		if !validPairCode(st.Config.PairCode) {
			code, err := newPairCode(s.Rand)
			if err != nil {
				return err
			}
			st.Config.PairCode, changed = code, true
		}
		if !validNonce(st.Config.Nonce) {
			n, err := newNonce(s.Rand)
			if err != nil {
				return err
			}
			st.Config.Nonce, changed = n, true
		}
		if mutate != nil {
			before := *st.Config
			if err := mutate(st.Config); err != nil {
				return err
			}
			if *st.Config != before {
				changed = true
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return Issue{}, fmt.Errorf("读写客户端配置状态失败: %w", err)
	}
	c := st.Config
	return Issue{
		Enabled: c.Enabled, PairCode: c.PairCode, Nonce: c.Nonce, LastSyncAt: c.LastSyncAt,
		RouterID: info.RouterID, RouterName: info.RouterName, LanAddresses: info.LanAddresses, PanelPort: info.PanelPort,
	}, nil
}

func validPairCode(s string) bool {
	if len(s) != pairCodeLen {
		return false
	}
	for _, c := range s {
		ok := false
		for _, a := range pairCodeAlphabet {
			if c == a {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func validNonce(s string) bool {
	if len(s) != nonceBytes*2 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func newPairCode(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	// 拒绝采样,字母表 36 个字符不能用取模偏差。
	out := make([]byte, 0, pairCodeLen)
	for len(out) < pairCodeLen {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", fmt.Errorf("生成随机数失败: %w", err)
		}
		if int(b[0]) >= 252 { // 252 = 7*36,丢掉尾巴保持均匀
			continue
		}
		out = append(out, pairCodeAlphabet[int(b[0])%len(pairCodeAlphabet)])
	}
	return string(out), nil
}

func newNonce(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, nonceBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
