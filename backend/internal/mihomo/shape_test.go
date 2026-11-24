package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 对拍向量 testdata/entrybypass_upstream.json:把上游 v0.1.462 Node 里的 TM / Nu / ii 原样抽出来在 Node 22 里,
// 喂同一批输入、记下它们的输出(只有输入和输出进了仓库,上游代码没有)。包含 151 条规则集 JSON、30 组兜底 IPv4、72 个入口旁路计划。
// 要重新生成:按 STATUS 日志 3e-4b-1 里的方法重做即可。

type shapeVal struct {
	Error string `json:"error"`
	RuleShape
}

type goldenFile struct {
	Shape []struct {
		Doc     string     `json:"doc"`
		Invalid bool       `json:"invalid"`
		Want    *RuleShape `json:"want"`
	} `json:"shape"`
	Fallback []struct {
		NeedSets  []string            `json:"needSets"`
		NeedCidrs []string            `json:"needCidrs"`
		Shapes    map[string]shapeVal `json:"shapes"`
		Want      []string            `json:"want"`
	} `json:"fallback"`
	Bypass []struct {
		Name      string              `json:"name"`
		Plan      BypassPlan          `json:"plan"`
		Shapes    map[string]shapeVal `json:"shapes"`
		UserLists json.RawMessage     `json:"userLists"`
		Existing  []string            `json:"existing"`
		Want      EntryBypass         `json:"want"`
	} `json:"bypass"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "entrybypass_upstream.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func shapeTable(m map[string]shapeVal) ShapeFunc {
	return func(_ context.Context, tag string) (RuleShape, error) {
		v, ok := m[tag]
		if !ok {
			return RuleShape{}, errors.New("本地没有这份规则集文件")
		}
		if v.Error != "" {
			return RuleShape{}, errors.New(v.Error)
		}
		return v.RuleShape.clone(), nil
	}
}

func TestExtractShapeMatchesUpstream(t *testing.T) {
	g := loadGolden(t)
	if len(g.Shape) < 100 {
		t.Fatalf("向量太少:%d", len(g.Shape))
	}
	for i, c := range g.Shape {
		got, err := ExtractShape([]byte(c.Doc))
		if c.Invalid {
			if err == nil {
				t.Errorf("#%d %s:JSON 不合法,应当报错", i, c.Doc)
			}
			continue
		}
		if err != nil {
			t.Errorf("#%d %s:意外报错 %v", i, c.Doc, err)
			continue
		}
		if !reflect.DeepEqual(got, *c.Want) {
			t.Errorf("#%d %s\n got  %+v\n want %+v", i, c.Doc, got, *c.Want)
		}
	}
}

func TestFallbackKeepIPv4MatchesUpstream(t *testing.T) {
	g := loadGolden(t)
	extra := append(append([]string{}, benchmarkSiblings...), fakeIPPools...)
	for i, c := range g.Fallback {
		got := FallbackKeepIPv4(context.Background(), shapeTable(c.Shapes), c.NeedSets, c.NeedCidrs, extra)
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("#%d sets=%v cidrs=%v\n got  %v\n want %v", i, c.NeedSets, c.NeedCidrs, got, c.Want)
		}
	}
}

func TestExtractShapeHandPicked(t *testing.T) {
	// 数组元素的「键」是下标;下标样的键先排;重复键位置留在第一次出现处。
	got, err := ExtractShape([]byte(`{"rules":[[1,2],{"b":1,"0":2,"a":3,"b":4}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0", "1", "b", "a"}; !reflect.DeepEqual(got.OtherKeys, want) {
		t.Errorf("otherKeys = %v,想要 %v", got.OtherKeys, want)
	}
	if _, err := ExtractShape([]byte(`{"rules":[`)); err == nil {
		t.Error("坏 JSON 应当报错")
	}
	// 切片永远不是 nil:JSON 里是 [] 不是 null。
	empty, _ := ExtractShape([]byte(`null`))
	b, _ := json.Marshal(empty)
	if string(b) != `{"cidrs":[],"unbounded":false,"domainKeys":[],"otherKeys":[]}` {
		t.Errorf("空形状的 JSON = %s", b)
	}
}

func TestJSNumber(t *testing.T) {
	cases := map[string]string{
		"100000000": "100000000", "1.5": "1.5", "1e21": "1e+21", "1e-7": "1e-7", "0.000001": "0.000001", "-3": "-3",
		"0": "0", "-0": "0", "1E5": "100000", "123456789012": "123456789012", "1.5e-7": "1.5e-7", "1e999": "Infinity",
	}
	for in, want := range cases {
		if got := jsString(json.RawMessage(in)); got != want {
			t.Errorf("jsString(%s) = %q,想要 %q", in, got, want)
		}
	}
}

// ---- ShapeReader ----

type revFS struct {
	*memFS
	revs   map[string]string
	revErr bool
	calls  int
}

func (r *revFS) FileRevision(p string) (string, error) {
	r.calls++
	if r.revErr {
		return "", errors.New("stat 失败")
	}
	return r.revs[p], nil
}

// plainFS 不带 FileRevision。
type plainFS struct{ *memFS }

type readerEnv struct {
	r        *ShapeReader
	fs       *revFS
	decoded  int
	docs     map[string]string
	failWith error
}

func newReaderEnv() *readerEnv {
	e := &readerEnv{fs: &revFS{memFS: newMemFS(), revs: map[string]string{}}, docs: map[string]string{}}
	e.r = &ShapeReader{
		FS: e.fs, GeoDir: "/geo", TmpDir: "/data/tmp",
		SrsPath: RuleSetPathFunc("/geo", "/user"),
		Decompile: func(_ context.Context, srs, out string) error {
			e.decoded++
			if e.failWith != nil {
				return e.failWith
			}
			tag := strings.TrimSuffix(filepath.Base(srs), ".srs")
			return e.fs.memFS.WriteFile(out, []byte(e.docs[tag]), 0o600)
		},
	}
	return e
}

func (e *readerEnv) addGeo(tag string, cidrs ...string) {
	e.fs.files["/geo/"+tag+".srs"] = []byte("srs")
	e.fs.revs["/geo/"+tag+".srs"] = "r1"
	quoted, _ := json.Marshal(cidrs)
	e.docs[tag] = `{"version":1,"rules":[{"ip_cidr":` + string(quoted) + `}]}`
}

func TestShapeReaderCacheAndInvalidation(t *testing.T) {
	e := newReaderEnv()
	e.addGeo("geoip-cn", "1.0.0.0/8", "2.0.0.0/8")
	ctx := context.Background()

	s1, err := e.r.Shape(ctx, "geoip-cn")
	if err != nil || !reflect.DeepEqual(s1.CIDRs, []string{"1.0.0.0/8", "2.0.0.0/8"}) {
		t.Fatalf("第一次:%+v %v", s1, err)
	}
	s1.CIDRs[0] = "改过了" // 调用方改返回值不能伤到缓存
	s2, _ := e.r.Shape(ctx, "geoip-cn")
	if e.decoded != 1 || s2.CIDRs[0] != "1.0.0.0/8" {
		t.Fatalf("应命中缓存且不受调用方修改影响:decoded=%d %+v", e.decoded, s2)
	}
	if len(e.fs.files) != 1 { // 只剩 .srs:还原用的临时文件删干净了
		t.Errorf("临时文件没删:%v", keysOf(e.fs.files))
	}

	// 文件被换掉(修订号变了):重新读,新内容生效。
	e.fs.revs["/geo/geoip-cn.srs"] = "r2"
	e.docs["geoip-cn"] = `{"rules":[{"ip_cidr":["9.0.0.0/8"]}]}`
	s3, _ := e.r.Shape(ctx, "geoip-cn")
	if e.decoded != 2 || !reflect.DeepEqual(s3.CIDRs, []string{"9.0.0.0/8"}) {
		t.Fatalf("修订号变了应重读:decoded=%d %+v", e.decoded, s3)
	}
	// 重读后的新结果又进缓存。
	if _, _ = e.r.Shape(ctx, "geoip-cn"); e.decoded != 2 {
		t.Errorf("新结果应进缓存,decoded=%d", e.decoded)
	}
}

func TestShapeReaderWhatIsNotCached(t *testing.T) {
	ctx := context.Background()

	// 用户规则集目录下的不缓存(上游只缓存随包规则库)。
	e := newReaderEnv()
	e.fs.files["/user/list-aaaa1111-ip.srs"] = []byte("srs")
	e.fs.revs["/user/list-aaaa1111-ip.srs"] = "r1"
	e.docs["list-aaaa1111-ip"] = `{"rules":[{"ip_cidr":["1.1.1.1"]}]}`
	e.r.Shape(ctx, "list-aaaa1111-ip")
	e.r.Shape(ctx, "list-aaaa1111-ip")
	if e.decoded != 2 {
		t.Errorf("用户规则集不该缓存:decoded=%d", e.decoded)
	}

	// FS 没有修订号能力 / 取修订号出错:不缓存。
	e = newReaderEnv()
	e.addGeo("geoip-cn", "1.0.0.0/8")
	e.fs.revErr = true
	e.r.Shape(ctx, "geoip-cn")
	e.r.Shape(ctx, "geoip-cn")
	if e.decoded != 2 {
		t.Errorf("取修订号出错不该缓存:decoded=%d", e.decoded)
	}
	e = newReaderEnv()
	e.addGeo("geoip-cn", "1.0.0.0/8")
	e.r.FS = plainFS{e.fs.memFS}
	e.r.Shape(ctx, "geoip-cn")
	e.r.Shape(ctx, "geoip-cn")
	if e.decoded != 2 {
		t.Errorf("FS 没有修订号能力不该缓存:decoded=%d", e.decoded)
	}

	// 没有任何网段的形状不缓存。
	e = newReaderEnv()
	e.addGeo("geoip-empty")
	e.docs["geoip-empty"] = `{"rules":[{"domain":["a"]}]}`
	e.r.Shape(ctx, "geoip-empty")
	e.r.Shape(ctx, "geoip-empty")
	if e.decoded != 2 {
		t.Errorf("没有网段的不该缓存:decoded=%d", e.decoded)
	}

	// 读完之后文件又变了(两次修订号不同):这次结果不缓存。
	e = newReaderEnv()
	e.addGeo("geoip-cn", "1.0.0.0/8")
	n := 0
	e.r.Decompile = func(_ context.Context, srs, out string) error {
		n++
		e.fs.revs[srs] = fmt.Sprintf("r%d", n+1)
		return e.fs.memFS.WriteFile(out, []byte(e.docs["geoip-cn"]), 0o600)
	}
	e.r.Shape(ctx, "geoip-cn")
	e.r.Shape(ctx, "geoip-cn")
	if n != 2 {
		t.Errorf("还原过程中文件变了不该缓存:还原了 %d 次", n)
	}
}

func TestShapeReaderEviction(t *testing.T) {
	ctx := context.Background()

	// 超过 32 份:丢最早放进去的。
	e := newReaderEnv()
	for i := 0; i < shapeCacheMax+1; i++ {
		tag := fmt.Sprintf("geoip-t%02d", i)
		e.addGeo(tag, "1.0.0.0/8")
		e.r.Shape(ctx, tag)
	}
	before := e.decoded
	e.r.Shape(ctx, "geoip-t00") // 最早的已被丢
	if e.decoded != before+1 {
		t.Errorf("第 33 份放进去后最早的应被丢掉")
	}
	e.r.Shape(ctx, "geoip-t32") // 最新的还在
	if e.decoded != before+1 {
		t.Errorf("最新的不该被丢")
	}

	// 网段合计超过 32768:丢最早的;单份超过 32768 的根本不缓存。
	e = newReaderEnv()
	big := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)
		}
		return out
	}
	e.addGeo("geoip-a", big(20000)...)
	e.addGeo("geoip-b", big(20000)...)
	e.addGeo("geoip-huge", big(shapeCIDRLimit+1)...)
	e.r.Shape(ctx, "geoip-a")
	e.r.Shape(ctx, "geoip-b") // 合计 40000 > 32768,a 被丢
	n := e.decoded
	e.r.Shape(ctx, "geoip-b")
	if e.decoded != n {
		t.Error("b 应在缓存里")
	}
	e.r.Shape(ctx, "geoip-a")
	if e.decoded != n+1 {
		t.Error("a 应已被挤掉")
	}
	e.r.Shape(ctx, "geoip-huge")
	n = e.decoded
	e.r.Shape(ctx, "geoip-huge")
	if e.decoded != n+1 {
		t.Error("超过上限的单份不该缓存")
	}
}

func TestShapeReaderErrors(t *testing.T) {
	ctx := context.Background()
	e := newReaderEnv()

	if _, err := e.r.Shape(ctx, "geoip-nope"); err == nil || err.Error() != "本地没有这份规则集文件" {
		t.Errorf("文件不在:%v", err)
	}
	e.addGeo("geoip-cn", "1.0.0.0/8")
	e.failWith = errors.New("无法读取现有规则集文件")
	if _, err := e.r.Shape(ctx, "geoip-cn"); err == nil || err.Error() != "解码失败:无法读取现有规则集文件" {
		t.Errorf("还原失败:%v", err)
	}
	if len(e.fs.files) != 1 {
		t.Errorf("还原失败也要清掉临时文件:%v", keysOf(e.fs.files))
	}
	e.failWith = nil
	e.docs["geoip-cn"] = `{"rules":[`
	if _, err := e.r.Shape(ctx, "geoip-cn"); err == nil || !strings.HasPrefix(err.Error(), "解码结果读不出来:") {
		t.Errorf("读不出来:%v", err)
	}
	if _, err := e.r.Shape(ctx, "../etc"); err == nil || !strings.Contains(err.Error(), "不合法的规则集名") {
		t.Errorf("不合法的名字:%v", err)
	}
}

func TestOSFSFileRevision(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.srs")
	fs := OSFS{}
	if _, err := fs.FileRevision(p); err == nil {
		t.Fatal("文件不在应报错")
	}
	if err := fs.WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	r1, err := fs.FileRevision(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(p, []byte("three"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r2, _ := fs.FileRevision(p); r1 == r2 {
		t.Errorf("内容换了修订号应变:%s", r1)
	}
	var _ Revisioner = OSFS{}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
