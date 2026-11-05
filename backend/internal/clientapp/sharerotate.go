package clientapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/timeflysoon/Open-Box/backend/internal/shareservers"
)

// POST /api/openbox/client-app/share-codes/{x}/rotate:换这台服务器的配对码,密码一起换并立即应用。
// 依据前端 api/clientApp.ts 的 rotateShareCode / OpenboxShareCodeRotated 的注释(「这台服务器的密码一起换,并立即应用。服务器不存在回 404」)
// 和 DESIGN 第 8 节、STATUS 条目;「哪个字段算密码」见 shareservers.Rekey。自己的设计,没有参照上游。
//
// 顺序(变更安全):一、先在档案里换密码(读改写在档案的存储锁里,不会和面板别处的保存互相覆盖,改完整份 servers 仍过保存校验);
// 二、再存新的配对码和加一后的代数,并丢掉旧封包(旧代数的取回立刻 404,旧链接也已不是这台服务器的密码);
// 三、锁外部署(可能要等二十多秒):内核在运行才部署,成功 restarted,不成功 failed 并写原因。
// 密码和配对码一旦换了就不回滚——前端据 failed 提示用户,旧密码本来就该作废;部署自己有「校验失败 / 启动失败自动换回旧配置」的保护。

// ErrServerNotFound 表示档案里没有这台共享服务器(接口回 404)。
var ErrServerNotFound = errors.New("没有这台共享服务器")

// 部署结果的三种应用情况,取值与前端 OpenboxShareCodeRotated.applied 一致。
const (
	AppliedSaved     = "saved"     // 只存了:没有要生效的东西,或内核没在运行(下次启动用新密码)
	AppliedRestarted = "restarted" // 存了并重新部署,内核已用上新密码
	AppliedFailed    = "failed"    // 存了但没生效,Message 是原因
)

// DeployResult 是一次部署的结果(deploystate.Service.Run 的返回值里本服务关心的三项)。
type DeployResult struct {
	OK      bool
	Busy    bool // 已有一次部署在进行,这次什么都没做
	Message string
}

// ShareRotated 是 POST share-codes/{x}/rotate 的响应,字段与前端 OpenboxShareCodeRotated 一一对应。
type ShareRotated struct {
	PairCode string         `json:"pairCode"`
	Gen      string         `json:"gen"`
	Server   map[string]any `json:"server"`
	Applied  string         `json:"applied"`
	Message  string         `json:"message,omitempty"`
}

// nextGen 把 16 位十六进制代数加一。
func nextGen(g string) (string, error) {
	n, err := strconv.ParseUint(g, 16, 64)
	if err != nil || !validGen(g) || n == ^uint64(0) {
		return "", fmt.Errorf("代数无法再加一: %q", g)
	}
	return fmt.Sprintf("%016x", n+1), nil
}

// RotateShare 处理 rotate。sid 不合格或档案里没有这台服务器返回 ErrServerNotFound;
// 档案里别的共享服务器有不合格的项(整份 servers 写回时被校验拦下)返回 *InputError。
func (s *Service) RotateShare(ctx context.Context, sid string) (ShareRotated, error) {
	if !validShareID(sid) {
		return ShareRotated{}, ErrServerNotFound
	}
	if s.UpdateServer == nil {
		return ShareRotated{}, ErrUnavailable
	}
	pair, err := newPairCode(s.Rand)
	if err != nil {
		return ShareRotated{}, err
	}

	// 同一把锁串行化所有共享服务器 App 码的写:两次 rotate、rotate 与 share-codes 不会互相踩。
	s.shareMu.Lock()
	var changed string
	server, err := s.UpdateServer(ctx, sid, func(m map[string]any) error {
		f, err := shareservers.Rekey(m, s.Rand)
		changed = f
		return err
	})
	if err != nil {
		s.shareMu.Unlock()
		return ShareRotated{}, err
	}
	var gen string
	var st state
	err = s.Store.Update(stateFile, &st, func() error {
		cur := st.Shares[sid]
		old := firstGen
		if cur != nil && validGen(cur.Gen) {
			old = cur.Gen
		}
		g, err := nextGen(old)
		if err != nil {
			return err
		}
		gen = g
		if st.Shares == nil {
			st.Shares = map[string]*shareState{}
		}
		st.Shares[sid] = &shareState{PairCode: pair, Gen: g} // 没有封包:旧代数取不回,新一代等面板下次交链接再封
		return nil
	})
	s.shareMu.Unlock()
	if err != nil {
		return ShareRotated{}, fmt.Errorf("服务器密码已更换,但保存新配对码失败,请再换一次: %w", err)
	}

	out := ShareRotated{PairCode: pair, Gen: gen, Server: server, Applied: AppliedSaved}
	if changed == "" {
		return out, nil // 没有可换的凭据(没设认证的 SOCKS5 / HTTP 入站):配对码换了,配置不用动
	}
	if en, isBool := server["enabled"].(bool); isBool && !en {
		return out, nil // 停用的服务器不在配置里
	}
	if s.Deploy == nil || s.Running == nil {
		return out, nil
	}
	running, err := s.Running(ctx)
	switch {
	case errors.Is(err, ErrUnavailable): // 面板不在路由器上,没有内核可重启
		return out, nil
	case err != nil:
		out.Applied, out.Message = AppliedFailed, "无法确认内核是否在运行,新密码已保存,请稍后在面板里手动部署: "+err.Error()
		return out, nil
	case !running:
		return out, nil // 内核没起:新密码在下次启动时生效
	}
	switch res := s.Deploy(ctx); {
	case res.OK:
		out.Applied = AppliedRestarted
	case res.Busy:
		out.Applied, out.Message = AppliedFailed, "已有一次部署在进行,新密码已保存,等它结束后请在面板里再部署一次"
	default:
		out.Applied, out.Message = AppliedFailed, res.Message
		if out.Message == "" {
			out.Message = "部署失败"
		}
	}
	return out, nil
}
