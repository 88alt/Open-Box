package clientapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 测试里把迭代次数压到 1000,真实默认值是 20 万次。
const testIter = 1000

func fileSvc(t *testing.T) *Service {
	t.Helper()
	s := newSvc(t, map[string]any{"serverInfo": map[string]any{"name": "客厅 & 书房"}})
	s.KDFIter = testIter
	s.Now = func() time.Time { return time.UnixMilli(1760000000000) }
	return s
}

func TestExportFileRoundTrip(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "192.168.5.1", "https://box.example.com/p/")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	info, _ := s.Info(ctx)
	if b.Format != BundleFormat || b.Version != BundleVersion || b.Router.ID != is.RouterID || b.Router.Name != "客厅 & 书房" || b.ExportedAt != 1760000000000 {
		t.Fatalf("文件头: %+v", b)
	}
	if b.KDF.Alg != SealKDF || b.KDF.Iterations != testIter || b.Data == "" {
		t.Fatalf("封包头: %+v data=%q", b.KDF, b.Data)
	}
	plain, err := OpenEnvelope(b.Seal(), is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	if p.Kind != "client-config" || p.GeneratedAt != b.ExportedAt || p.Router.ID != is.RouterID || p.Router.PanelPort != 3036 ||
		len(p.Router.LanSubnets) != 1 || p.Router.LanSubnets[0] != "192.168.5.0/24" || p.Router.ServerInfo.Name != "客厅 & 书房" {
		t.Fatalf("明文: %+v", p)
	}
	if len(p.ShareRegions) != len(info.ShareRegions) || len(p.ShareRegions) != 3 {
		t.Fatalf("地区分流应是默认三组: %d", len(p.ShareRegions))
	}
	// 配对码不出现在不封的部分
	raw, _ := json.Marshal(b)
	if bytes.Contains(raw, []byte(is.PairCode)) {
		t.Fatal("配对码不能出现在导出文件里")
	}
}

func TestExportFileWrongOrStaleCodeFails(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	wrong := "AAAA"
	if is.PairCode == wrong {
		wrong = "BBBB"
	}
	if _, err := OpenEnvelope(b.Seal(), wrong, testIter, testIter); !errors.Is(err, ErrSealOpen) {
		t.Fatalf("错的配对码: %v", err)
	}
	// 换码后旧文件失效,新文件能用新码解开
	is2, err := s.RotateClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEnvelope(b.Seal(), is2.PairCode, testIter, testIter); !errors.Is(err, ErrSealOpen) {
		t.Fatalf("旧文件应已失效: %v", err)
	}
	b2, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEnvelope(b2.Seal(), is2.PairCode, testIter, testIter); err != nil {
		t.Fatalf("新文件应能解开: %v", err)
	}
	if b2.Seal().KDF.Salt == b.Seal().KDF.Salt {
		t.Fatal("每次封包 salt 都是随机的")
	}
}

func TestOpenEnvelopeRejectsTamperAndDowngrade(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	open := func(e Envelope, min, max int) error {
		_, err := OpenEnvelope(e, is.PairCode, min, max)
		return err
	}
	if err := open(b.Seal(), testIter, testIter); err != nil {
		t.Fatal(err)
	}
	// 改密文
	bad := b.Seal()
	raw, _ := b64.DecodeString(bad.Data)
	raw[len(raw)/2] ^= 0xff
	bad.Data = b64.EncodeToString(raw)
	if !errors.Is(open(bad, testIter, testIter), ErrSealOpen) {
		t.Fatal("改密文应解不开")
	}
	// 改 salt
	bad = b.Seal()
	salt, _ := b64.DecodeString(bad.KDF.Salt)
	salt[0] ^= 0xff
	bad.KDF.Salt = b64.EncodeToString(salt)
	if !errors.Is(open(bad, testIter, testIter), ErrSealOpen) {
		t.Fatal("改 salt 应解不开")
	}
	for name, e := range map[string]Envelope{
		"算法":    {KDF: KDFParams{Alg: "scrypt", Iterations: testIter, Salt: b.Seal().KDF.Salt}, Data: b.Seal().Data},
		"坏salt": {KDF: KDFParams{Alg: SealKDF, Iterations: testIter, Salt: "!!"}, Data: b.Seal().Data},
		"短data": {KDF: b.Seal().KDF, Data: b64.EncodeToString([]byte("short"))},
	} {
		if !errors.Is(open(e, testIter, testIter), ErrSealOpen) {
			t.Fatalf("%s 应解不开", name)
		}
	}
	if !errors.Is(open(b.Seal(), testIter+1, testIter+10), ErrSealOpen) {
		t.Fatal("迭代次数低于下限应拒")
	}
	if !errors.Is(open(b.Seal(), 1, testIter-1), ErrSealOpen) {
		t.Fatal("迭代次数高于上限应拒")
	}
}

func TestSealUsesFreshIVAndSalt(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	a, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Data == b.Data {
		t.Fatal("每次应换 iv/密文")
	}
	if a.KDF.Salt == b.KDF.Salt {
		t.Fatal("每次应换 salt")
	}
}

func TestImportCodeMatchesFrontendFormat(t *testing.T) {
	// 与前端 buildClientImportCode 的字段顺序和 encodeURIComponent 转义一致
	got := ImportCode("0123456789abcdef0123456789abcdef", "客厅 & 书房", "192.168.5.1", 3036, "https://box.example.com:8443/p/", "00ff00ff00ff00ff")
	want := "openbox://import?v=2&r=0123456789abcdef0123456789abcdef&n=%E5%AE%A2%E5%8E%85%20%26%20%E4%B9%A6%E6%88%BF&lan=192.168.5.1&port=3036&pub=https%3A%2F%2Fbox.example.com%3A8443%2Fp%2F&p=00ff00ff00ff00ff"
	if got != want {
		t.Fatalf("\n得到 %s\n想要 %s", got, want)
	}
	// 没有 pub 就不带;lan 为空仍带 lan=
	got = ImportCode("r", "Open-Box", "", 80, "", "n")
	if got != "openbox://import?v=2&r=r&n=Open-Box&lan=&port=80&p=n" {
		t.Fatalf("没有 pub / lan: %s", got)
	}
	// 能被标准库还原
	u, err := url.Parse(ImportCode("r", "a b+c/d", "10.0.0.1", 1, "http://h/", "n"))
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("n") != "a b+c/d" || q.Get("pub") != "http://h/" || q.Get("p") != "n" || u.Host != "import" {
		t.Fatalf("还原: %v %s", q, u.Host)
	}
	// encodeURIComponent 不转义的符号
	if escapeComponent("-_.!~*'()") != "-_.!~*'()" || escapeComponent("a/b?c=d#e") != "a%2Fb%3Fc%3Dd%23e" {
		t.Fatal("转义集合不对")
	}
}

func TestExportFileValidatesInput(t *testing.T) {
	s := fileSvc(t)
	for name, c := range map[string][2]string{
		"lan 带空格":     {"a b", ""},
		"lan 带 &":     {"1.2.3.4&x=1", ""},
		"lan 太长":      {strings.Repeat("a", 254), ""},
		"pub 不是 http": {"", "ftp://h/"},
		"pub 无主机":     {"", "https:///x"},
		"pub 带账号":     {"", "https://u:p@h/"},
		"pub 太长":      {"", "https://h/" + strings.Repeat("a", 2048)},
		"pub 不是网址":    {"", "::::"},
	} {
		_, err := s.ExportFile(context.Background(), c[0], c[1])
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 应是 InputError,得到 %v", name, err)
		}
	}
	// 合法的:IPv6、主机名、带路径的 https
	for _, c := range [][2]string{{"fe80::1", ""}, {"router.lan", "http://1.2.3.4:3036/"}, {" 10.0.0.1 ", " https://h.example/a/ "}} {
		if _, err := s.ExportFile(context.Background(), c[0], c[1]); err != nil {
			t.Errorf("%v 应合法: %v", c, err)
		}
	}
}

func TestExportFileInputErrorWritesNothing(t *testing.T) {
	s := fileSvc(t)
	if _, err := s.ExportFile(context.Background(), "a b", ""); err == nil {
		t.Fatal("应报错")
	}
	// 参数不合格时连配对码都没生成(先校验再碰存储)
	var st state
	if ok, err := s.Store.Load(stateFile, &st); err != nil || (ok && st.Config != nil) {
		t.Fatalf("参数不合格不该写存储: %v %+v", err, st.Config)
	}
}
