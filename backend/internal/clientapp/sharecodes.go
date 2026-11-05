package clientapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"
)

// 共享网络服务器的 App 码:POST /api/openbox/client-app/share-codes(面板出码)和 App 用的公开取回接口
// GET /client/v1/share(App 扫码后拿 sid + 代数 + 配对码取回节点配置)。依据前端 api/clientApp.ts 对 OpenboxShareCodeSeal 的注释、
// helper/clientAppCode.ts 对 share 码(v=3,字段 g 与 sid)的说明和 DESIGN 第 8 节;封包本身沿用 seal.go(对齐上游 v0.1.402 的外形),
// 取回接口的外壳(format / 字段)是自己定的,没有参照上游。
//
// 每台服务器一份状态:自己的配对码(同 client-config 的长度和字母表)、代数 gen(16 位小写十六进制,换码时加一)、
// 以及最近一次面板交来的节点分享链接封成的封包。上游封包没有 AAD、salt 每个封包随机,所以「这个封包属于哪台路由器 /
// 哪台服务器 / 哪一代」改为写进明文,解开后由 OpenShare 核对,对不上一律按解不开处理:
//
//	明文 = {"v":1,"kind":"share-link","generatedAt":<毫秒>,"routerId":…,"sid":…,"gen":…,"link":…}
//
// 链接由前端按服务器档案拼好后随请求交来(服务器列表一变前端就重新交),后端不自己拼链接:拼法属于前端,两边各写一份只会漂移。
//
// 状态里不存明文链接,只存封包和链接的 HMAC(密钥由路由器 ID、服务器 id、代数和配对码派生,不需要 PBKDF2):
// 同一条链接再交一次就认得出来、不重封不写盘(面板每次打开页面都会交一遍)。

const (
	shareDomain = "open-box/share-code/v1"
	// MaxShareLinkLen 是单条节点分享链接的字符数上限(前端类型注释:超过 4096 字符的回包里没有)。
	MaxShareLinkLen = 4096
	// MaxShareLinks 是一次请求最多交几台服务器的链接(与档案里服务器条数的上限一致)。
	MaxShareLinks = 64
	maxShareIDLen = 64
	// ShareFormat 是公开取回接口响应里 format 字段的值。
	ShareFormat  = "open-box-share"
	ShareVersion = 1
	genLen       = 16
	firstGen     = "0000000000000001"
)

type shareState struct {
	PairCode string    `json:"pairCode"`
	Gen      string    `json:"gen"`
	LinkMAC  string    `json:"linkMac,omitempty"`
	Seal     *Envelope `json:"seal,omitempty"`
	SealedAt int64     `json:"sealedAt,omitempty"`
}

// ShareSeal 是 POST share-codes 响应里每台服务器的一项,字段与前端 OpenboxShareCodeSeal 一一对应。
// Sealed 是封包的 JSON 文本(前端当不透明字符串,v=3 的码里已不带它)。
type ShareSeal struct {
	PairCode string `json:"pairCode"`
	Gen      string `json:"gen"`
	Sealed   string `json:"sealed"`
}

// ShareResult 是公开取回接口的响应体。
type ShareResult struct {
	Format     string   `json:"format"`
	V          int      `json:"v"`
	RouterID   string   `json:"routerId"`
	SID        string   `json:"sid"`
	Gen        string   `json:"gen"`
	ExportedAt int64    `json:"exportedAt"`
	Seal       Envelope `json:"seal"`
}

type sharePlain struct {
	V           int    `json:"v"`
	Kind        string `json:"kind"`
	GeneratedAt int64  `json:"generatedAt"`
	RouterID    string `json:"routerId"`
	SID         string `json:"sid"`
	Gen         string `json:"gen"`
	Link        string `json:"link"`
}

// OpenShare 用这台服务器的配对码解开共享服务器的封包,返回明文 JSON。App 一侧的做法也是这样;本后端自己用在测试里。
// 解开后核对明文里的路由器 ID、服务器 id、代数,对不上按解不开处理(别家 / 别的服务器 / 换码前的封包都不认)。
// 迭代次数的上下限含义同 OpenEnvelope。
func OpenShare(e Envelope, pairCode, routerID, sid, gen string, minIter, maxIter int) ([]byte, error) {
	plain, err := OpenEnvelope(e, pairCode, minIter, maxIter)
	if err != nil {
		return nil, err
	}
	var p sharePlain
	if json.Unmarshal(plain, &p) != nil || p.Kind != "share-link" || p.RouterID != routerID || p.SID != sid || p.Gen != gen {
		return nil, ErrSealOpen
	}
	return plain, nil
}

// shareMAC 是链接的 HMAC:密钥绑路由器 ID、服务器 id、代数和配对码,换码(代数加一、配对码换新)后自然对不上。
func shareMAC(routerID, sid, gen, pairCode, link string) string {
	k := sha256.Sum256([]byte(shareDomain + "\x00mac\x00" + routerID + "\x00" + sid + "\x00" + gen + "\x00" + pairCode))
	m := hmac.New(sha256.New, k[:])
	m.Write([]byte(link))
	return hex.EncodeToString(m.Sum(nil))
}

// validShareID 与档案里服务器 id 的规则一致:字母、数字、下划线、短横线,1–64 个字符。
func validShareID(id string) bool {
	if id == "" || len(id) > maxShareIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validGen(g string) bool {
	if len(g) != genLen {
		return false
	}
	for _, c := range g {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// profileServerIDs 读档案里现有服务器的 id。读不到档案就报错(不猜)。
func (s *Service) profileServerIDs(ctx context.Context) (map[string]bool, error) {
	prof, err := s.Profile(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取档案失败: %w", err)
	}
	ids := map[string]bool{}
	list, _ := prof["servers"].([]any)
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				ids[id] = true
			}
		}
	}
	return ids, nil
}

type sharePrep struct {
	sid    string
	pair   string // 本次用的配对码和代数;写回时状态里对不上就丢弃(期间被别处改了)
	gen    string
	fresh  bool // 状态里原本没有这台服务器
	mac    string
	seal   *Envelope
	sealed bool // 本次新封了一份(false = 沿用已存的)
	at     int64
}

// ShareCodes 处理 POST share-codes:links 是服务器 id → 它的节点分享链接。
// 档案里没有的服务器、id 不合格的、链接为空或超过 4096 字符(或不是合法文本)的,回包里没有,也不建状态。
// 第一次为某台服务器出码时生成它的配对码和代数;之后配对码不变,直到换码。返回每台服务器的 {pairCode, gen, sealed}。
func (s *Service) ShareCodes(ctx context.Context, links map[string]string) (map[string]ShareSeal, error) {
	if len(links) > MaxShareLinks {
		return nil, &InputError{Msg: fmt.Sprintf("一次最多 %d 台服务器", MaxShareLinks)}
	}
	routerID, err := s.RouterID(ctx)
	if err != nil {
		return nil, err
	}
	have, err := s.profileServerIDs(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(links))
	for id, link := range links {
		if validShareID(id) && have[id] && link != "" && utf8.ValidString(link) && utf8.RuneCountInString(link) <= MaxShareLinkLen {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	var snap state
	if _, err := s.Store.Load(stateFile, &snap); err != nil {
		return nil, fmt.Errorf("读取客户端配置状态失败: %w", err)
	}
	preps := make([]sharePrep, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := sharePrep{sid: id}
		cur := snap.Shares[id]
		if cur != nil && validPairCode(cur.PairCode) && validGen(cur.Gen) {
			p.pair, p.gen = cur.PairCode, cur.Gen
		} else {
			code, err := newPairCode(s.Rand)
			if err != nil {
				return nil, err
			}
			p.pair, p.gen, p.fresh, cur = code, firstGen, true, nil
		}
		p.mac = shareMAC(routerID, id, p.gen, p.pair, links[id])
		if cur != nil && cur.Seal != nil && cur.LinkMAC == p.mac && cur.Seal.KDF.Iterations == s.kdfIter() {
			p.seal, p.at = cur.Seal, cur.SealedAt
		} else {
			now := s.now().UnixMilli()
			plain, err := json.Marshal(sharePlain{V: 1, Kind: "share-link", GeneratedAt: now, RouterID: routerID, SID: id, Gen: p.gen, Link: links[id]})
			if err != nil {
				return nil, fmt.Errorf("编码节点配置失败: %w", err)
			}
			env, err := seal(p.pair, s.kdfIter(), plain, s.Rand)
			if err != nil {
				return nil, fmt.Errorf("封包失败: %w", err)
			}
			p.seal, p.at, p.sealed = &env, now, true
		}
		preps = append(preps, p)
	}

	// 写回:只写本次新建的和重新封过的;状态里这台服务器的配对码 / 代数已不是本次用的(期间被换了)就丢弃这一项。
	// 同时清掉档案里已经没有的服务器的状态。没有任何变化就不写盘。
	out := make(map[string]ShareSeal, len(preps))
	var st state
	err = s.Store.Update(stateFile, &st, func() error {
		changed := false
		for _, p := range preps {
			cur := st.Shares[p.sid]
			if (cur == nil) != p.fresh || (cur != nil && (cur.PairCode != p.pair || cur.Gen != p.gen)) {
				continue
			}
			if p.fresh || p.sealed {
				if st.Shares == nil {
					st.Shares = map[string]*shareState{}
				}
				st.Shares[p.sid] = &shareState{PairCode: p.pair, Gen: p.gen, LinkMAC: p.mac, Seal: p.seal, SealedAt: p.at}
				changed = true
			}
		}
		for id := range st.Shares {
			if !have[id] {
				delete(st.Shares, id)
				changed = true
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return nil, fmt.Errorf("保存共享服务器的配对码失败: %w", err)
	}
	for _, p := range preps {
		cur := st.Shares[p.sid]
		if cur == nil || cur.PairCode != p.pair || cur.Gen != p.gen || cur.Seal == nil {
			continue // 写回时被丢弃了,回包里不给(前端下次再取)
		}
		raw, err := json.Marshal(cur.Seal)
		if err != nil {
			return nil, fmt.Errorf("编码封包失败: %w", err)
		}
		out[p.sid] = ShareSeal{PairCode: cur.PairCode, Gen: cur.Gen, Sealed: string(raw)}
	}
	return out, nil
}

// Share 处理 App 的公开取回:GET /client/v1/share?sid=&g=。口径与 Sync 相同:同步开关关着 ErrDisabled(只读状态,不进密码运算),
// 来源 IP 被锁 *LimitedError,服务器不存在 / 已不在档案里 / 代数对不上 / 还没出过码一律 ErrSyncNotFound 并记一次失败。
// 返回的只是封包:没有这台服务器的配对码解不开。
func (s *Service) Share(ctx context.Context, sid, gen, ip string) (ShareResult, error) {
	var st state
	if _, err := s.Store.Load(stateFile, &st); err != nil {
		return ShareResult{}, fmt.Errorf("读取客户端配置状态失败: %w", err)
	}
	if c := st.Config; c == nil || !c.Enabled {
		return ShareResult{}, ErrDisabled
	}
	if d := s.syncLimit.blocked(ip, s.now()); d > 0 {
		return ShareResult{}, &LimitedError{After: d}
	}
	var sh *shareState
	if validShareID(sid) && validGen(gen) {
		sh = st.Shares[sid]
	}
	if sh == nil || sh.Seal == nil || !validRouterID(st.RouterID) || subtle.ConstantTimeCompare([]byte(gen), []byte(sh.Gen)) != 1 {
		s.syncLimit.fail(ip, s.now())
		return ShareResult{}, ErrSyncNotFound
	}
	have, err := s.profileServerIDs(ctx)
	if err != nil {
		return ShareResult{}, err
	}
	if !have[sid] {
		s.syncLimit.fail(ip, s.now())
		return ShareResult{}, ErrSyncNotFound
	}
	return ShareResult{Format: ShareFormat, V: ShareVersion, RouterID: st.RouterID, SID: sid, Gen: sh.Gen, ExportedAt: sh.SealedAt, Seal: *sh.Seal}, nil
}
