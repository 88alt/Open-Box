package release

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 打包侧(与 Parse / Verify 对称):把一份已经摆好的目录写成 manifest.json、tar.gz 与 SHA256SUMS。
//
// 为什么用 Go 而不是 tar / sha256sum 拼 shell:文件模式必须由我们显式写进 tar 头,
// 不能读磁盘权限——Windows / MSYS 上 chmod 是空操作(STATUS 2026-11-25 记过这个坑),
// 在那种机器上打出来的包 bin/* 会没有执行位,装到路由器上直接跑不起来。

// WriteManifest 扫 root 下的所有文件(跳过 manifest.json 自己),填好 m.Files 再写 manifest.json。
// 文件按路径排序,保证同一份输入每次生成的清单逐字节相同。权限位取自磁盘。
func WriteManifest(root string, m *Manifest) error { return WriteManifestWith(root, m, nil) }

// WriteManifestWith 与 WriteManifest 相同,但权限位由 mode 决定(传 nil = 用磁盘上的)。
//
// 打包时应当传一个规则函数而不是信任磁盘:Windows / MSYS 上 chmod 是空操作,读出来一律 0666,
// 打出来的包 bin/* 就没有执行位,装到路由器上跑不起来(STATUS 2026-11-25 记过这个坑)。
func WriteManifestWith(root string, m *Manifest, mode func(path string) string) error {
	if m == nil {
		return fmt.Errorf("manifest 为空")
	}
	var files []File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("打包目录里有不是普通文件的东西:%s", rel)
		}
		if rel == ManifestName {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum, err := FileSHA256(p)
		if err != nil {
			return err
		}
		perm := fmt.Sprintf("%04o", info.Mode().Perm())
		if mode != nil {
			perm = mode(rel)
		}
		files = append(files, File{
			Path:   rel,
			SHA256: sum,
			Size:   info.Size(),
			Mode:   perm,
		})
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("打包目录 %s 里没有文件", root)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	m.Files = files
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(root, ManifestName), data, 0o644)
}

// CreateArchive 把 root 打成 tar.gz:顶层目录是 topDir,压缩包里是 topDir/manifest.json 与清单里的每个文件。
// 文件模式按清单写(不读磁盘权限),修改时间统一用 builtAt(零值则用 Unix 零点),同样为了可复现。
func CreateArchive(root, out string, m *Manifest, topDir string, builtAt time.Time) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if topDir == "" || strings.ContainsAny(topDir, "/\\") {
		return fmt.Errorf("顶层目录名不合法:%q", topDir)
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	if !builtAt.IsZero() {
		gz.Header.ModTime = builtAt
	}
	tw := tar.NewWriter(gz)

	write := func(name, diskPath string, mode fs.FileMode) error {
		src, err := os.Open(diskPath)
		if err != nil {
			return err
		}
		defer src.Close()
		info, err := src.Stat()
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:     topDir + "/" + name,
			Mode:     int64(mode.Perm()),
			Size:     info.Size(),
			Typeflag: tar.TypeReg,
			ModTime:  builtAt,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, src)
		return err
	}

	// manifest.json 排在最前面:解包工具边解边读清单时不用先跳到末尾
	if err := write(ManifestName, filepath.Join(root, ManifestName), 0o644); err != nil {
		return fmt.Errorf("写 %s:%w", ManifestName, err)
	}
	for _, file := range m.Files {
		mode, err := parseMode(file.Mode)
		if err != nil {
			return fmt.Errorf("%s 的 mode 不对:%w", file.Path, err)
		}
		disk := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := write(file.Path, disk, mode); err != nil {
			return fmt.Errorf("写 %s:%w", file.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// WriteSums 把「文件名 → 校验和」写成 sha256sum 格式,按文件名排序,与 ParseSums 对称。
func WriteSums(w io.Writer, sums Sums) error {
	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(w, "%s  %s\n", sums[name], name); err != nil {
			return err
		}
	}
	return nil
}

// WriteSumsFile 在 path 写一份 SHA256SUMS。files 是「归档名 → 磁盘路径」,校验和现场算。
func WriteSumsFile(path string, files map[string]string) error {
	sums := make(Sums, len(files))
	for name, disk := range files {
		sum, err := FileSHA256(disk)
		if err != nil {
			return err
		}
		sums[name] = sum
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteSums(f, sums)
}

func parseMode(s string) (fs.FileMode, error) {
	var n uint32
	if _, err := fmt.Sscanf(s, "%o", &n); err != nil {
		return 0, err
	}
	if n > 0o777 {
		return 0, fmt.Errorf("权限位超出 0777:%q", s)
	}
	return fs.FileMode(n), nil
}

// CountGeoFiles 数一个规则库目录里的分类文件:<分类名>.srs 里以 geosite- 开头算 geosite,geoip- 开头算 geoip。
// 别的文件(说明、清单自己)不算。
func CountGeoFiles(dir string) (GeoCounts, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return GeoCounts{}, err
	}
	var c GeoCounts
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".srs") {
			continue
		}
		switch {
		case strings.HasPrefix(e.Name(), "geosite-"):
			c.Geosite++
		case strings.HasPrefix(e.Name(), "geoip-"):
			c.Geoip++
		}
	}
	return c, nil
}

// WriteGeoData 在 dir 下写 geodata.json(规则库清单),后端的 GET /update/status 从这里取
// geoVersion / geoDate / geoCounts。它也会被写进 manifest.files,所以要在写清单之前调。
func WriteGeoData(dir string, g GeoData) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, GeoDataName), append(b, '\n'), 0o644)
}
