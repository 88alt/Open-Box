// Package timezone 实现 GET / PUT /api/openbox/system/timezone:看和改路由器的系统时区
// (后端的定时任务按路由器本地时间的钟点跑)。响应形状取自前端 api/openbox.ts 的 OpenboxTimezoneState,
// 没有参照上游。
//
// 时区名单、国家代码、别名和每个时区的 POSIX 时区串来自内置的 zones.json(用 gen/main.go 从 IANA tzdata 生成):
// 路由器上不一定装了 tzdata,而 OpenWrt 要同时写时区名和 POSIX 时区串才能生效。
//
//   - OpenWrt:读 / 写 `uci system.@system[0].zonename` 和 `.timezone`,然后 `/etc/init.d/system reload` 让它生效;
//   - systemd:`timedatectl show -p Timezone` / `timedatectl set-timezone`;
//   - 偏移和本地时间不自己算,直接问系统的 `date`(OpenWrt 上显式带上 TZ),反映的是系统真实状态。
//
// 修改只接受名单里的名字(别名先换成名单里的名字),不会把任意字符串拼进命令;
// OpenWrt 上改失败会把原来的值写回去。
package timezone

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

//go:embed zones.json
var zonesJSON []byte

type zoneInfo struct {
	TZ string `json:"tz"`
	CC string `json:"cc"`
}

type table struct {
	Version string              `json:"version"`
	Zones   map[string]zoneInfo `json:"zones"`
	Aliases map[string]string   `json:"aliases"`
}

var (
	tbl      table
	zoneList []string
)

// utcAliases 是 tzdata 里不算「链接」、但用户和系统常见的 UTC 写法,统一成 UTC。
var utcAliases = []string{"Etc/UTC", "Etc/UCT", "UCT", "Zulu", "Etc/Zulu", "Etc/Universal", "Universal", "GMT", "Etc/GMT", "Etc/Greenwich", "Greenwich"}

func init() {
	if err := json.Unmarshal(zonesJSON, &tbl); err != nil {
		panic("timezone: 内置的 zones.json 坏了: " + err.Error())
	}
	for _, a := range utcAliases {
		if _, has := tbl.Aliases[a]; !has {
			tbl.Aliases[a] = "UTC"
		}
	}
	zoneList = make([]string, 0, len(tbl.Zones))
	for z := range tbl.Zones {
		zoneList = append(zoneList, z)
	}
	sort.Strings(zoneList)
}

// 错误。
var (
	ErrUnknownZone = errors.New("不认识的时区")
	ErrUnsupported = errors.New("这台机器没有识别到受支持的服务管理器(OpenWrt 或 systemd),不能修改时区")
)

// Normalize 把时区名(含别名)换成名单里的名字;不认识返回 false。
func Normalize(zone string) (string, bool) {
	zone = strings.TrimSpace(zone)
	if _, ok := tbl.Zones[zone]; ok {
		return zone, true
	}
	if t, ok := tbl.Aliases[zone]; ok {
		return t, true
	}
	return "", false
}

// State 对应前端的 OpenboxTimezoneState。
type State struct {
	Zone      string            `json:"zone"`
	Offset    string            `json:"offset"` // 形如 +08:00
	Local     string            `json:"local"`  // 形如 2026-10-08 10:03:52(路由器本地时间)
	Platform  string            `json:"platform"`
	Zones     []string          `json:"zones"`
	Countries map[string]string `json:"countries"` // 时区 → 国家 / 地区两字母代码(UTC 没有)
}

// Service 看和改系统时区。
type Service struct {
	Init system.InitKind
	Run  system.Runner // nil = system.ExecRunner
	mu   sync.Mutex
}

func (s *Service) run(ctx context.Context, name string, args ...string) (system.RunResult, error) {
	r := s.Run
	if r == nil {
		r = system.ExecRunner
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r(cctx, name, args...)
}

// safePosix 只放行 POSIX 时区串里会出现的字符(含 <+06> 这种带尖括号的缩写),
// 值来自内置名单或系统自己的配置,这里再挡一道。
var safePosix = regexp.MustCompile(`^[A-Za-z0-9<>+:,./_-]{1,80}$`)

func (s *Service) base() State {
	cc := make(map[string]string, len(tbl.Zones))
	for z, i := range tbl.Zones {
		if i.CC != "" {
			cc[z] = i.CC
		}
	}
	return State{Platform: string(s.Init), Zones: zoneList, Countries: cc}
}

// Get 返回当前状态。读不到时区名(比如这台机器没有服务管理器)zone 为空串,名单照给;不当作错误。
func (s *Service) Get(ctx context.Context) (State, error) {
	st := s.base()
	var posix string
	switch s.Init {
	case system.InitOpenWrt:
		st.Zone = s.uci(ctx, "zonename")
		posix = s.uci(ctx, "timezone")
	case system.InitSystemd:
		if r, err := s.run(ctx, "timedatectl", "show", "-p", "Timezone", "--value"); err == nil && r.Code == 0 {
			st.Zone = strings.TrimSpace(r.Stdout)
		}
	}
	if n, ok := Normalize(st.Zone); ok {
		st.Zone = n
		if posix == "" {
			posix = tbl.Zones[n].TZ
		}
	}
	st.Offset, st.Local = s.clock(ctx, posix)
	return st, nil
}

func (s *Service) uci(ctx context.Context, key string) string {
	r, err := s.run(ctx, "uci", "-q", "get", "system.@system[0]."+key)
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

// clock 问系统的 date 要偏移和本地时间。OpenWrt 上拿得到 POSIX 时区串就显式设 TZ,不依赖本进程的环境。
func (s *Service) clock(ctx context.Context, posix string) (offset, local string) {
	name, args := "date", []string{"+%z|%Y-%m-%d %H:%M:%S"}
	if s.Init == system.InitOpenWrt && safePosix.MatchString(posix) {
		name, args = "env", []string{"TZ=" + posix, "date", args[0]}
	}
	r, err := s.run(ctx, name, args...)
	if err != nil || r.Code != 0 {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(r.Stdout), "|", 2)
	if len(parts) != 2 || len(parts[0]) != 5 || (parts[0][0] != '+' && parts[0][0] != '-') {
		return "", ""
	}
	return parts[0][:3] + ":" + parts[0][3:], parts[1]
}

// Set 改系统时区,返回改完读回来的状态。
func (s *Service) Set(ctx context.Context, zone string) (State, error) {
	canon, ok := Normalize(zone)
	if !ok {
		return State{}, ErrUnknownZone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.Init {
	case system.InitOpenWrt:
		if err := s.setOpenWrt(ctx, canon); err != nil {
			return State{}, err
		}
	case system.InitSystemd:
		r, err := s.run(ctx, "timedatectl", "set-timezone", canon)
		if err != nil {
			return State{}, fmt.Errorf("运行 timedatectl 失败: %w", err)
		}
		if r.Code != 0 {
			return State{}, fmt.Errorf("timedatectl set-timezone 失败(退出码 %d):%s", r.Code, strings.TrimSpace(r.Stderr))
		}
	default:
		return State{}, ErrUnsupported
	}
	st, err := s.Get(ctx)
	if err != nil {
		return State{}, err
	}
	if st.Zone != canon {
		return State{}, fmt.Errorf("已执行修改,但读回来的时区是 %q 而不是 %q,请在路由器上确认", st.Zone, canon)
	}
	return st, nil
}

// setOpenWrt 写 zonename 和 timezone、提交、让系统重新加载。
//   - 改之前先看 system 包里有没有别人暂存、还没提交的修改(比如 LuCI 里点了保存但没应用):`uci commit system`
//     会把它们一并提交,所以有就拒绝,不动;
//   - 任何一步失败:先 `uci revert system` 丢掉没提交的暂存,再把原来的值写回去、提交、重新加载。
func (s *Service) setOpenWrt(ctx context.Context, zone string) error {
	posix := tbl.Zones[zone].TZ
	if !safePosix.MatchString(posix) || !safePosix.MatchString(zone) {
		return fmt.Errorf("内置的时区数据不合法: %s", zone) // 理论上不会发生,挡一道
	}
	r, err := s.run(ctx, "uci", "changes", "system")
	if err != nil {
		return fmt.Errorf("读不到 uci 的暂存修改,为安全起见没有改时区: %w", err)
	}
	if r.Code != 0 {
		return fmt.Errorf("uci changes 失败(退出码 %d),为安全起见没有改时区: %s", r.Code, strings.TrimSpace(r.Stderr))
	}
	if strings.TrimSpace(r.Stdout) != "" {
		return errors.New("system 配置里有还没提交的修改(比如 LuCI 里保存了但没应用),为避免一并提交,没有改时区;请先在 LuCI 里应用或还原这些修改")
	}
	oldZone, oldTZ := s.uci(ctx, "zonename"), s.uci(ctx, "timezone")
	apply := func(z, tz string) error {
		for _, kv := range [][2]string{{"zonename", z}, {"timezone", tz}} {
			var r system.RunResult
			var err error
			if kv[1] == "" {
				r, err = s.run(ctx, "uci", "-q", "delete", "system.@system[0]."+kv[0])
				if err == nil && r.Code == 1 { // 本来就没有
					r.Code = 0
				}
			} else {
				r, err = s.run(ctx, "uci", "set", "system.@system[0]."+kv[0]+"="+kv[1])
			}
			if err != nil || r.Code != 0 {
				return fmt.Errorf("uci 写 %s 失败: %s", kv[0], failText(r, err))
			}
		}
		if r, err := s.run(ctx, "uci", "commit", "system"); err != nil || r.Code != 0 {
			return fmt.Errorf("uci commit 失败: %s", failText(r, err))
		}
		if r, err := s.run(ctx, "/etc/init.d/system", "reload"); err != nil || r.Code != 0 {
			return fmt.Errorf("system reload 失败: %s", failText(r, err))
		}
		return nil
	}
	if err := apply(zone, posix); err != nil {
		_, _ = s.run(ctx, "uci", "revert", "system") // 丢掉没提交的暂存
		if rbErr := apply(oldZone, oldTZ); rbErr != nil {
			_, _ = s.run(ctx, "uci", "revert", "system")
			return fmt.Errorf("%w;而且恢复原来的时区也失败了(%v),请在路由器上检查 /etc/config/system", err, rbErr)
		}
		return fmt.Errorf("%w(已恢复原来的时区)", err)
	}
	return nil
}

func failText(r system.RunResult, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("退出码 %d %s", r.Code, strings.TrimSpace(r.Stderr))
}
