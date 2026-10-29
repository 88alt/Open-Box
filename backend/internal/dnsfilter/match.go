package dnsfilter

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 「这个域名是不是被已应用的名单拦下的」。dnsmasq 的查询日志里看不出内核的过滤动了手脚(被拦的域名和真正不存在的域名
// 一样是 NXDOMAIN),所以查询记录要拿域名回头对一遍名单。对法与 core 的 dnsFilter 生成的规则一致(自己的设计,
// 依据 core/internal/dnsfilter 的规则形状和 sing-box 官方文档对 domain / domain_suffix / domain_regex 的说明):
//
//   - 先看带 $important 的拦截,再看普通拦截;
//   - 带 $important 的拦截只被用户放行和带 $important 的放行排除;普通拦截还要再被普通放行排除;
//   - 放行不分名单,所有启用的名单合在一起算;
//   - 命中的名单取设置里排在前面的那一份。

type matchSet struct {
	exact  map[string]struct{}
	suffix map[string]struct{}
	regex  []*regexp.Regexp
}

func (m *matchSet) empty() bool { return len(m.exact) == 0 && len(m.suffix) == 0 && len(m.regex) == 0 }

func (m *matchSet) add(typ, value string) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" {
		return
	}
	switch typ {
	case TypeDomain:
		if m.exact == nil {
			m.exact = map[string]struct{}{}
		}
		m.exact[value] = struct{}{}
	case TypeDomainSuffix:
		if m.suffix == nil {
			m.suffix = map[string]struct{}{}
		}
		m.suffix[value] = struct{}{}
	case TypeDomainRegex:
		// 正则按原样编(不转小写:转了会改变 \D 之类的意思);编不过的跳过,core 那边也编不过
		if re, err := regexp.Compile(strings.TrimSpace(strings.TrimSuffix(value, "."))); err == nil {
			m.regex = append(m.regex, re)
		}
	}
}

func (m *matchSet) has(d string) bool {
	if _, ok := m.exact[d]; ok {
		return true
	}
	// 后缀:域名本身,以及它的每一级父域
	for s := d; s != ""; {
		if _, ok := m.suffix[s]; ok {
			return true
		}
		i := strings.IndexByte(s, '.')
		if i < 0 {
			break
		}
		s = s[i+1:]
	}
	for _, re := range m.regex {
		if re.MatchString(d) {
			return true
		}
	}
	return false
}

type listMatch struct {
	name       string
	blockImp   matchSet
	blockPlain matchSet
}

// Matcher 判断域名是否被一组名单拦截。创建后只读,可并发使用。
type Matcher struct {
	lists    []listMatch
	userAll  matchSet
	allowImp matchSet
	allow    matchSet
}

// NewMatcher 按设置里的名单顺序和各份名单的解析结果建 Matcher。没启用、没有解析结果的名单略过。
func NewMatcher(s Settings, parsed map[string]*Parsed) *Matcher {
	m := &Matcher{}
	for _, l := range s.Lists {
		p := parsed[l.ID]
		if !l.Enabled || p == nil {
			continue
		}
		lm := listMatch{name: l.Name}
		if lm.name == "" {
			lm.name = l.ID
		}
		for _, e := range p.Entries {
			switch {
			case e.Action == ActionAllow && e.Important:
				m.allowImp.add(e.Type, e.Value)
			case e.Action == ActionAllow:
				m.allow.add(e.Type, e.Value)
			case e.Important:
				lm.blockImp.add(e.Type, e.Value)
			default:
				lm.blockPlain.add(e.Type, e.Value)
			}
		}
		m.lists = append(m.lists, lm)
	}
	for _, d := range s.AllowDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		switch {
		case d == "":
		case strings.HasPrefix(d, "*."):
			m.userAll.add(TypeDomainSuffix, strings.TrimPrefix(d, "*."))
		default:
			m.userAll.add(TypeDomain, d)
		}
	}
	return m
}

// Check 判断 domain 是否被拦截,被拦时返回命中的名单名。
func (m *Matcher) Check(domain string) (bool, string) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if d == "" || m == nil {
		return false, ""
	}
	if m.userAll.has(d) || m.allowImp.has(d) {
		return false, ""
	}
	for i := range m.lists {
		if !m.lists[i].blockImp.empty() && m.lists[i].blockImp.has(d) {
			return true, m.lists[i].name
		}
	}
	if m.allow.has(d) {
		return false, ""
	}
	for i := range m.lists {
		if !m.lists[i].blockPlain.empty() && m.lists[i].blockPlain.has(d) {
			return true, m.lists[i].name
		}
	}
	return false, ""
}

// matcherTTL 是 Classify 多久重新看一次「设置和名单有没有变」。
const matcherTTL = 30 * time.Second

type matcherCache struct {
	mu    sync.Mutex
	key   string
	m     *Matcher
	until time.Time
}

// invalidate 让下一次 Classify 重新看设置和名单。
func (c *matcherCache) invalidate() {
	c.mu.Lock()
	c.until = time.Time{}
	c.mu.Unlock()
}

// matcherKey 是建 Matcher 用到的输入的指纹:设置的指纹加上每份启用名单的更新时间。
func matcherKey(s Settings, st *state) string {
	var b strings.Builder
	b.WriteString(SettingsKey(s))
	ids := make([]string, 0, len(s.Lists))
	for _, l := range s.Lists {
		if l.Enabled {
			ids = append(ids, l.ID+"="+strconv.FormatInt(st.Lists[l.ID].UpdatedAt, 10))
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		b.WriteString("|" + id)
	}
	return b.String()
}

// Classify 给查询记录用:这个域名是不是被已应用的名单拦下的,被拦时给出名单名。
// 过滤没启用、还没应用过、或者读不到设置 / 名单缓存时一律回 false(宁可少标「被拦截」,也不乱标)。
// 名单从缓存文件读(不联网),结果缓存 30 秒;设置或名单更新后下一次会重建。
func (sv *Service) Classify(domain string) (bool, string) {
	m := sv.currentMatcher()
	if m == nil {
		return false, ""
	}
	return m.Check(domain)
}

func (sv *Service) currentMatcher() *Matcher {
	c := &sv.mc
	c.mu.Lock()
	defer c.mu.Unlock()
	now := sv.now()
	if now.Before(c.until) {
		return c.m
	}
	c.until = now.Add(matcherTTL)
	if sv.Profiles == nil {
		c.m, c.key = nil, ""
		return nil
	}
	prof, err := sv.Profiles.Get(context.Background())
	if err != nil {
		c.m, c.key = nil, ""
		return nil
	}
	s := FromProfile(prof)
	st, err := sv.load()
	if err != nil || !s.Enabled || st.Applied == nil || !st.Applied.Enabled {
		c.m, c.key = nil, ""
		return nil
	}
	key := matcherKey(s, st)
	if c.m != nil && key == c.key {
		return c.m
	}
	parsed := map[string]*Parsed{}
	for _, l := range s.Lists {
		if !l.Enabled {
			continue
		}
		b, err := os.ReadFile(sv.cachePath(l.ID))
		if err != nil {
			continue
		}
		if p, err := ParseList(b); err == nil {
			parsed[l.ID] = p
		}
	}
	c.m, c.key = NewMatcher(s, parsed), key
	return c.m
}
