package rulelist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []Entry
		skipped int
	}{
		{"经典名单", "# c\nDOMAIN,a.com\nDOMAIN-SUFFIX,B.com\nDOMAIN-KEYWORD,Foo\nIP-CIDR,1.2.3.4/24,no-resolve\nIP-CIDR6,2001:db8::1/32\nDOMAIN-REGEX,^a\\d+\\.x$\n", []Entry{
			{"domain", "a.com"}, {"domain_suffix", "b.com"}, {"domain_keyword", "foo"}, {"ip_cidr", "1.2.3.0/24"}, {"ip_cidr", "2001:db8::/32"}, {"domain_regex", `^a\d+\.x$`},
		}, 0},
		{"忽略不认得的并计数", "PROCESS-NAME,x.exe\nGEOIP,CN\nDOMAIN,ok.com\nUSER-AGENT,foo\n", []Entry{{"domain", "ok.com"}}, 3},
		{"yaml payload", "payload:\n  - '+.google.com'\n  - \"DOMAIN,x.org\"\n  - 1.1.1.1\n  - .cdn.net # 注释\n", []Entry{
			{"domain_suffix", "google.com"}, {"domain", "x.org"}, {"ip_cidr", "1.1.1.1/32"}, {"domain_suffix", "cdn.net"},
		}, 0},
		{"裸域名与通配", "*.a.com\nplain.com\nPlain.com\n; x\n// y\n\n", []Entry{{"domain_suffix", "a.com"}, {"domain", "plain.com"}}, 0},
		{"BOM 与 CRLF", "\ufeffDOMAIN,a.com\r\nDOMAIN,b.com\r\n", []Entry{{"domain", "a.com"}, {"domain", "b.com"}}, 0},
		{"重复只留首个", "DOMAIN,a.com\nDOMAIN,a.com\nDOMAIN-SUFFIX,a.com\n", []Entry{{"domain", "a.com"}, {"domain_suffix", "a.com"}}, 0},
		{"非法写法跳过", "DOMAIN,bad host\nDOMAIN,\nIP-CIDR,300.1.1.1/8\nhttp://x.com/a\n", nil, 0},
	}
	for _, c := range cases {
		got, err := Parse([]byte(c.in))
		if c.want == nil {
			if !errors.Is(err, ErrEmpty) {
				t.Errorf("%s: 期望 ErrEmpty, got %v %v", c.name, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got.Entries, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got.Entries, c.want)
		}
		if got.Skipped != c.skipped {
			t.Errorf("%s: skipped=%d want %d", c.name, got.Skipped, c.skipped)
		}
	}
}

func TestParseBinaryAndEmpty(t *testing.T) {
	if _, err := Parse([]byte("MRS\x01\x00\x00\x02\xff\xfe")); !errors.Is(err, ErrBinary) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse([]byte("  \n# only comment\n")); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err=%v", err)
	}
}

func TestFilter(t *testing.T) {
	es := []Entry{{"domain", "A.example.com"}, {"ip_cidr", "1.2.3.0/24"}}
	if got := Filter(es, " EXAMPLE "); len(got) != 1 || got[0].Type != "domain" {
		t.Fatalf("%v", got)
	}
	if got := Filter(es, ""); len(got) != 2 {
		t.Fatal("空关键词应返回全部")
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("DOMAIN,a.com\n"))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("a", MaxBodyBytes+10)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if b, err := Fetch(ctx, nil, srv.URL+"/ok"); err != nil || string(b) != "DOMAIN,a.com\n" {
		t.Fatalf("%q %v", b, err)
	}
	var fe *FetchError
	if _, err := Fetch(ctx, nil, srv.URL+"/404"); !errors.As(err, &fe) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Fetch(ctx, nil, srv.URL+"/big"); !errors.As(err, &fe) {
		t.Fatalf("超限应报错: %v", err)
	}
	var in *InputError
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "http://"} {
		if _, err := Fetch(ctx, nil, bad); !errors.As(err, &in) {
			t.Errorf("%q: err=%v", bad, err)
		}
	}
}

func TestServiceCache(t *testing.T) {
	var calls atomic.Int32
	s := NewService(func(_ context.Context, u string) ([]byte, error) {
		calls.Add(1)
		return []byte("DOMAIN," + strings.TrimPrefix(u, "http://") + "\n"), nil
	})
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Load(ctx, "http://a.com"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("缓存内不该重复拉取: %d", calls.Load())
	}
	now = now.Add(cacheTTL + time.Second)
	_, _ = s.Load(ctx, "http://a.com")
	if calls.Load() != 2 {
		t.Fatalf("过期应重拉: %d", calls.Load())
	}
	for i := 0; i < cacheMax+3; i++ {
		now = now.Add(time.Second)
		_, _ = s.Load(ctx, "http://h"+string(rune('a'+i))+".com")
	}
	if len(s.cache) > cacheMax {
		t.Fatalf("缓存超限: %d", len(s.cache))
	}
}

func TestServiceDoesNotCacheErrors(t *testing.T) {
	var calls atomic.Int32
	s := NewService(func(context.Context, string) ([]byte, error) { calls.Add(1); return nil, &FetchError{Msg: "x"} })
	_, _ = s.Load(context.Background(), "http://a.com")
	_, _ = s.Load(context.Background(), "http://a.com")
	if calls.Load() != 2 {
		t.Fatalf("失败不应缓存: %d", calls.Load())
	}
}
