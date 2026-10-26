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
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
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
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), rs)
	return true
}
