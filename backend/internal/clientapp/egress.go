package clientapp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// 按出口公网 IP 判国家(POST /api/openbox/client-app/egress-country),结果缓存在 client-app.json 的
// egressCountry 里,GET client-app/info 原样带回(前端一天内不再重判)。
// 只做一次只读的查询(复用 regiondetect 的三家接口),不改任何设置。自己的设计,没有参照上游。

// ErrUnavailable 表示没有接上出口判断(面板不在路由器上 / 没配服务管理)。接口回 503。
var ErrUnavailable = errors.New("出口国家判断不可用")

// ErrNotRunning 表示内核没在运行。前端注释:内核没起时回 503。
var ErrNotRunning = errors.New("内核没在运行,没法判断出口国家")

// DetectError 是查询出口 IP / 国家失败(接口回 502)。
type DetectError struct{ Msg string }

func (e *DetectError) Error() string { return e.Msg }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// validEgress 判断缓存里的值能不能用:IP 能解析、国家是两位大写字母、时刻为正。
func validEgress(e *EgressCountry) bool {
	if e == nil || e.At <= 0 || len(e.Country) != 2 {
		return false
	}
	if _, err := netip.ParseAddr(e.IP); err != nil {
		return false
	}
	for _, c := range e.Country {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// cachedEgress 读上次判出的国家;没判过、读不出或值不合格都是 nil(Info 里是 null,不因此让整页打不开)。
func (s *Service) cachedEgress() *EgressCountry {
	var st state
	if ok, err := s.Store.Load(stateFile, &st); err != nil || !ok || !validEgress(st.Egress) {
		return nil
	}
	e := *st.Egress
	return &e
}

// DetectEgressCountry 判一次出口国家并缓存。出口 IP 和上次一样就沿用上次的国家、只把时刻更新成现在
// (At 是「最后确认的时刻」:前端据此一天内不再重判,不刷新的话过了一天每次打开页面都会重判);
// 不同(或没有缓存)才换国家。内核没在运行、没接上判断、状态读不出来都不判:不猜。
func (s *Service) DetectEgressCountry(ctx context.Context) (EgressCountry, error) {
	if s.DetectEgress == nil || s.Running == nil {
		return EgressCountry{}, ErrUnavailable
	}
	running, err := s.Running(ctx)
	if err != nil {
		return EgressCountry{}, fmt.Errorf("读取内核服务状态失败: %w", err)
	}
	if !running {
		return EgressCountry{}, ErrNotRunning
	}
	ip, cc, err := s.DetectEgress(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return EgressCountry{}, ctx.Err()
		}
		return EgressCountry{}, &DetectError{Msg: err.Error()}
	}
	addr, perr := netip.ParseAddr(ip)
	if perr != nil || len(cc) != 2 {
		return EgressCountry{}, &DetectError{Msg: "出口判断没有给出有效的 IP 或国家代码"}
	}
	fresh := EgressCountry{IP: addr.String(), Country: upper2(cc), At: s.now().UnixMilli()}
	if !validEgress(&fresh) {
		return EgressCountry{}, &DetectError{Msg: "出口判断没有给出有效的 IP 或国家代码"}
	}
	var st state
	var out EgressCountry
	err = s.Store.Update(stateFile, &st, func() error {
		if validEgress(st.Egress) && st.Egress.IP == fresh.IP {
			kept := *st.Egress
			if fresh.At <= kept.At { // 时钟倒退:不把时刻改小
				out = kept
				return errNoChange
			}
			kept.At = fresh.At
			st.Egress, out = &kept, kept
			return nil
		}
		st.Egress = &fresh
		out = fresh
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return EgressCountry{}, fmt.Errorf("保存出口国家失败: %w", err)
	}
	return out, nil
}

func upper2(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}
