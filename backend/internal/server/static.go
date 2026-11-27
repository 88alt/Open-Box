package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// StaticHandler 提供前端构建产物(panel/dist)。
//   - 存在的文件原样返回;
//   - 不存在且没有扩展名的路径(前端路由)回落到 index.html;
//   - 不存在且带扩展名的路径(缺失的 js / css / 图片)返回 404,避免把 HTML 当脚本执行。
//
// index.html 不缓存;assets/ 下是带哈希的文件,长期缓存。
func StaticHandler(dir string) http.Handler {
	fsys := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "不支持的请求方法", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if serveFile(w, r, fsys, name) {
			return
		}
		if name != "index.html" && path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		if !serveFile(w, r, fsys, "index.html") {
			http.Error(w, "前端文件不存在,请用 --web-dir 指定 panel/dist 目录: "+dir, http.StatusServiceUnavailable)
		}
	})
}

// serveFile 尝试返回 fsys 里的普通文件;不存在(或是目录)返回 false。
//
// 上游发布包里的 panel/dist 有一部分文件**只有 .gz 预压缩版**(入口用的 index-*.js / vendor-*.js、
// 若干 css 都是),没有同名未压缩文件。以前这里只按精确文件名找,于是 index.html 引用的入口脚本
// 全部 404、面板整页白屏(2026-11-27 在真机上踩到)。现在的规则:
//   - 有未压缩文件就原样返回(和以前一样);
//   - 没有、且有 <name>.gz、并且客户端接受 gzip 时,返回 .gz 并带上 Content-Encoding: gzip;
//   - 客户端不接受 gzip 时仍按缺失处理(不去解压,保持实现简单,浏览器都接受 gzip)。
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		if gz, ok := openGzipFallback(r, fsys, name); ok {
			defer gz.f.Close()
			rs, ok := gz.f.(io.ReadSeeker)
			if !ok {
				return false
			}
			writeStatic(w, r, name, rs, gz.info, true)
			return true
		}
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	writeStatic(w, r, name, rs, info, false)
	return true
}

// writeStatic 设缓存头并把内容吐出去;precompressed 为真时标记响应用 gzip 编码。
// 注意 ServeContent 会按扩展名猜 Content-Type,这里传的是**原始文件名**(不带 .gz),
// 所以 .js 仍然被认成 JavaScript、.css 仍然是 CSS,不会因为压缩后缀变成 octet-stream。
func writeStatic(w http.ResponseWriter, r *http.Request, name string, rdr io.ReadSeeker, info fs.FileInfo, precompressed bool) {
	if precompressed {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), rdr)
}

type gzipFile struct {
	f    fs.File
	info fs.FileInfo
}

// openGzipFallback 在只有 <name>.gz 时返回它;客户端(或 HEAD 请求)不接受 gzip 时不算命中。
// HEAD 也要求客户端声明 gzip:否则头部会声称 gzip,而后续 GET 又拿不到,前后不一致。
func openGzipFallback(r *http.Request, fsys fs.FS, name string) (gzipFile, bool) {
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return gzipFile{}, false
	}
	f, err := fsys.Open(name + ".gz")
	if err != nil {
		return gzipFile{}, false
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return gzipFile{}, false
	}
	return gzipFile{f: f, info: info}, true
}
