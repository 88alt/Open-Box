package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 上游发布包的 panel/dist 里,入口用的那几个文件只有 .gz 预压缩版:
// assets/index-CmavJE4Y.js、vendor-lib-*.js、vendor-vue-*.js、vendor-ui-*.js、index-*.css
// 都没有同名未压缩文件。以前 serveFile 只按精确名字找,于是 index.html 引用的入口脚本全部 404、
// 面板整页白屏(2026-11-27 真机上踩到)。这组测试盯住三件事:
//  1. 客户端接受 gzip 时,只有 .gz 的文件能取到,内容解压后正确,且带 Content-Encoding: gzip;
//  2. Content-Type 按**原始文件名**判(.js 仍是 JavaScript,不是 octet-stream);
//  3. 客户端不接受 gzip 时按缺失处理(404),不去解压。
func TestStaticGzipOnlyAsset(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}

	const jsBody = "console.log('entry');\n"
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(jsBody)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	// 只有 .gz,没有同名未压缩文件——这正是入口脚本的真实形态
	if err := os.WriteFile(filepath.Join(assets, "index-ABC.js.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// 对照组:有未压缩文件的,仍走原来的路径
	if err := os.WriteFile(filepath.Join(assets, "plain-DEF.js"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := StaticHandler(dir)

	t.Run("接受 gzip 时返回解压后的内容", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-ABC.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("应返回 200,拿到 %d(只有 .gz 时必须能取到)", w.Code)
		}
		if got := w.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding 应为 gzip,拿到 %q", got)
		}
		if ct := w.Header().Get("Content-Type"); ct == "" || ct == "application/octet-stream" {
			t.Fatalf("Content-Type 应按原始文件名判成 JavaScript,拿到 %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Fatalf("assets/ 下应长期缓存,拿到 %q", cc)
		}
		// httptest.NewRecorder 不收自动解压,内容应是压缩后的字节;解压比对
		zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatalf("响应体不是合法 gzip: %v", err)
		}
		defer zr.Close()
		var out bytes.Buffer
		if _, err := out.ReadFrom(zr); err != nil {
			t.Fatal(err)
		}
		if out.String() != jsBody {
			t.Fatalf("解压后内容不对: %q", out.String())
		}
	})

	t.Run("不接受 gzip 时按缺失处理", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-ABC.js", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("客户端不接受 gzip 时应 404,拿到 %d", w.Code)
		}
	})

	t.Run("有未压缩文件时照旧", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/plain-DEF.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "plain" {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
		if enc := w.Header().Get("Content-Encoding"); enc != "" {
			t.Fatalf("未压缩文件不该带 Content-Encoding,拿到 %q", enc)
		}
	})

	t.Run("前端路由仍回落到 index.html", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/proxies", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("<!doctype html>")) {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
	})
}
