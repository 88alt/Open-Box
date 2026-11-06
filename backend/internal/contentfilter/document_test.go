package contentfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sd(id, host string, rules ...Rule) SetData {
	return SetData{ID: id, Name: "set " + id, Dialect: Surge, Hostnames: []string{host}, Rules: rules}
}

func TestBuildDocument(t *testing.T) {
	drop := Rule{ID: "r-1", Dialect: Loon, Pattern: "^a", Action: "drop"}
	script := Rule{ID: "r-2", Dialect: Surge, Pattern: "^b", Action: "script",
		Params: map[string]any{"phase": "response", "sourceUrl": "https://s/a.js", "sha256": "abc", "requiresBody": false}}
	a, b := sd("a", "x.com.", drop, script), sd("b", "X.com", drop)
	a.Scripts = map[string]string{"abc": "console.log('<ok>&')"}
	got, err := BuildDocument([]SetData{a, b})
	if err != nil {
		t.Fatal(err)
	}
	d := got.Document
	if d.Version != 1 || len(d.Hostnames) != 1 || d.Hostnames[0] != "x.com" || len(d.Rules) != 3 {
		t.Fatalf("%+v", d)
	}
	if d.Rules[0].ID != "a:r-1" || d.Rules[0].RuleSetID != "a" || d.Rules[2].ID != "b:r-1" || d.Rules[2].RuleSetID != "b" {
		t.Fatalf("%+v", d.Rules)
	}
	// 同一集合里 id 撞了加 -2
	c, _ := BuildDocument([]SetData{sd("a", "x.com", drop, drop, drop)})
	if ids := fmt.Sprint(c.Document.Rules[0].ID, c.Document.Rules[1].ID, c.Document.Rules[2].ID); ids != "a:r-1a:r-1-2a:r-1-3" {
		t.Fatal(ids)
	}
	// 正文:不转义 <>&;drop 没有 params 键;元数据的 sha256 / size 就是正文的
	body := string(got.Body)
	if !strings.Contains(body, "<ok>&") || strings.Contains(body, `"params":null`) || !strings.HasPrefix(body, `{"version":1,"hostnames":["x.com"],"rules":[{"id":"a:r-1","ruleSetId":"a","dialect":"loon"`) {
		t.Fatal(body)
	}
	sum := sha256.Sum256(got.Body)
	m := got.Metadata
	if m.SHA256 != hex.EncodeToString(sum[:]) || m.Size != len(got.Body) || m.Version != 1 {
		t.Fatalf("%+v", m)
	}
	if len(m.Sets) != 2 || m.Sets[0].ID != "a" || m.Sets[0].RuleCount != 2 || m.Sets[0].ScriptCount != 1 || m.Sets[1].ScriptCount != 0 {
		t.Fatalf("%+v", m.Sets)
	}
	// 同样的输入得到同样的字节
	again, _ := BuildDocument([]SetData{a, b})
	if string(again.Body) != body {
		t.Fatal("同样的输入应当得到同样的正文")
	}
	var back Document
	if err := json.Unmarshal(got.Body, &back); err != nil || len(back.Rules) != 3 || back.Scripts["abc"] == "" {
		t.Fatalf("%v %+v", err, back)
	}
}

func TestBuildDocumentEmptyAndLimits(t *testing.T) {
	e, err := BuildDocument(nil)
	if err != nil || string(e.Body) != `{"version":1,"hostnames":[],"rules":[],"scripts":{}}` || len(e.Metadata.Sets) != 0 || e.Metadata.Hostnames == nil {
		t.Fatalf("%v %s %+v", err, e.Body, e.Metadata)
	}
	r := Rule{ID: "r-1", Dialect: Surge, Pattern: "^a", Action: "drop"}
	many := make([]Rule, MaxDocRules)
	for i := range many {
		many[i] = r
	}
	if _, err := BuildDocument([]SetData{sd("a", "x.com", many...)}); err != nil {
		t.Fatalf("正好 %d 条应当可以: %v", MaxDocRules, err)
	}
	if _, err := BuildDocument([]SetData{sd("a", "x.com", append(many, r)...)}); err != ErrDocTooLarge {
		t.Fatalf("超过 %d 条应当拒绝: %v", MaxDocRules, err)
	}
	hosts := make([]string, MaxDocHostnames+1)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%d.com", i)
	}
	if _, err := BuildDocument([]SetData{{ID: "a", Hostnames: hosts, Rules: []Rule{r}}}); err != ErrDocTooLarge {
		t.Fatalf("主机名超限: %v", err)
	}
	big := SetData{ID: "a", Hostnames: []string{"x.com"}, Rules: []Rule{r}, Scripts: map[string]string{"h": strings.Repeat("x", MaxDocBytes)}}
	if _, err := BuildDocument([]SetData{big}); err != ErrDocTooLarge {
		t.Fatalf("正文超限: %v", err)
	}
}

// 随包的三份默认,按上游的口径合并:能出文档、在上限内、两次一样,元数据的数目和随包数据对得上。
func TestBuildDocumentFromShippedDefaults(t *testing.T) {
	var sets []SetData
	for _, s := range shippedSets(t) {
		var rules []Rule
		for _, w := range s.Rules {
			b, _ := json.Marshal(w)
			var r Rule
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			rules = append(rules, r)
		}
		sets = append(sets, SetData{ID: s.ID, Name: s.Name, Dialect: Dialect(s.Dialect), Hostnames: s.Hostnames, Rules: rules, Scripts: s.Scripts})
	}
	got, err := BuildDocument(sets)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range sets {
		total += len(s.Rules)
	}
	if len(got.Document.Rules) != total || got.Metadata.Size != len(got.Body) || got.Metadata.Size > MaxDocBytes {
		t.Fatalf("规则 %d/%d size %d", len(got.Document.Rules), total, got.Metadata.Size)
	}
	for _, r := range got.Document.Rules {
		if r.Action == "script" {
			sha := r.Params["sha256"].(string)
			if _, ok := got.Document.Scripts[sha]; !ok {
				t.Fatalf("规则 %s 的脚本 %s 不在文档里", r.ID, sha)
			}
		}
	}
	t.Logf("默认三份合并:%d 条规则、%d 个主机名、%d 个脚本,正文 %d 字节", len(got.Document.Rules), len(got.Document.Hostnames), len(got.Document.Scripts), len(got.Body))
}
