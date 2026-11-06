package contentfilter

// 结构化落盘与脚本抓取(第五步 b 的一部分)。
// 每份集合: <Dir>/<id>.txt 原文, <Dir>/<id>.json 结构化结果。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// MaxScriptBytes 是一份集合里所有脚本正文合计上限(2 MiB)。
const MaxScriptBytes = 2 << 20

// storedSet 是磁盘上的结构化结果。
type storedSet struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Dialect   Dialect           `json:"dialect"`
	Hostnames []string          `json:"hostnames"`
	Rules     []Rule            `json:"rules"`
	Scripts   map[string]string `json:"scripts"`
	Declared  []Argument        `json:"declared,omitempty"`
	Skipped   int               `json:"skipped"`
	// Incomplete:脚本有 sourceUrl 但抓不到,列表里可提示「立即更新」。
	Incomplete bool `json:"incomplete,omitempty"`
}

func (s *Service) structPath(id string) string {
	return filepath.Join(s.Dir, id+".json")
}

func (s *Service) writeStruct(st storedSet) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("创建规则目录失败: %w", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return storeWrite(s.structPath(st.ID), b)
}

func (s *Service) readStruct(id string) (storedSet, bool, error) {
	b, err := os.ReadFile(s.structPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return storedSet{}, false, nil
		}
		return storedSet{}, false, err
	}
	var st storedSet
	if err := json.Unmarshal(b, &st); err != nil {
		return storedSet{}, false, err
	}
	return st, true, nil
}

func (s *Service) removeStruct(id string) {
	_ = os.Remove(s.structPath(id))
}

// storeWrite 与文本同样走原子写(复用 store 包的话在 service 里已有 WriteFileAtomic)。
func storeWrite(path string, b []byte) error {
	return store.WriteFileAtomic(path, b, 0o600)
}

// buildStored 分析文本、抓脚本,得到可落盘的结构。text 是原文(未代入);values 是用户参数。
func (s *Service) buildStored(ctx context.Context, id, name string, d Dialect, text, sourceURL string, values map[string]string) (storedSet, *Analysis, error) {
	a, err := Analyze(d, text, values, sourceURL)
	if err != nil {
		return storedSet{}, nil, err
	}
	if len(a.Rules) == 0 && len(a.Hostnames) == 0 {
		return storedSet{}, a, &InputError{Msg: "没有可运行的规则或 MITM 域名,请检查格式与规则内容"}
	}
	scripts := map[string]string{}
	incomplete := false
	total := 0
	for i := range a.Rules {
		r := &a.Rules[i]
		if r.Action != "script" || r.Params == nil {
			continue
		}
		src, _ := r.Params["sourceUrl"].(string)
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		body, err := s.fetchSource(ctx, src)
		if err != nil {
			incomplete = true
			continue
		}
		sum := sha256.Sum256(body)
		h := hex.EncodeToString(sum[:])
		if _, ok := scripts[h]; !ok {
			total += len(body)
			if total > MaxScriptBytes {
				return storedSet{}, a, &InputError{Msg: "脚本正文合计超过 2 MiB,请减少脚本或拆分规则集"}
			}
			scripts[h] = string(body)
		}
		r.Params["sha256"] = h
	}
	st := storedSet{
		ID: id, Name: name, Dialect: d,
		Hostnames: a.Hostnames, Rules: a.Rules, Scripts: scripts,
		Declared: a.Declared, Skipped: a.Skipped(), Incomplete: incomplete,
	}
	return st, a, nil
}

func (s *Service) fetchSource(ctx context.Context, u string) ([]byte, error) {
	if s.Fetch != nil {
		return s.Fetch(ctx, u)
	}
	text, err := FetchSource(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	return []byte(text), nil
}

// toSetData 把落盘结构转成合并文档用的 SetData。
func (st storedSet) toSetData() SetData {
	sc := st.Scripts
	if sc == nil {
		sc = map[string]string{}
	}
	return SetData{
		ID: st.ID, Name: st.Name, Dialect: st.Dialect,
		Hostnames: st.Hostnames, Rules: st.Rules, Scripts: sc,
	}
}

// sourceErrMsg 把 source_* 原因码翻成给前端的中文。
func sourceErrMsg(err error) string {
	var se *SourceError
	if !asSource(err, &se) {
		return "抓取规则失败: " + err.Error()
	}
	switch {
	case se.Reason == "source_url_invalid":
		return "规则链接无效"
	case se.Reason == "source_url_not_https":
		return "规则链接只支持 https"
	case se.Reason == "source_url_private":
		return "规则链接不能指向本机或内网地址"
	case se.Reason == "source_too_many_redirects":
		return "规则链接重定向次数过多"
	case se.Reason == "source_too_large":
		return "规则内容超过 512 KiB"
	case se.Reason == "source_fetch_timeout":
		return "抓取规则超时"
	case strings.HasPrefix(se.Reason, "source_content_type_unsupported"):
		return "规则链接返回的类型不受支持"
	case strings.HasPrefix(se.Reason, "source_fetch_failed"):
		return "抓取规则失败"
	case strings.HasPrefix(se.Reason, "source_redirect"):
		return "规则链接重定向无效"
	default:
		return "抓取规则失败"
	}
}

func asSource(err error, target **SourceError) bool {
	return errors.As(err, target)
}
