// Package release 解析与校验安装包里的 manifest.json 与 SHA256SUMS。
//
// 格式是自己定的(M6 第三节),不对应上游,也不读上游的任何产物:
//
//	format    恒为 "open-box-release"
//	version   发布版本号,如 v0.1.482-main.1
//	upstream  上游号,如 482
//	arch      amd64 / arm64
//	builtAt   RFC3339
//	components  {backend:{version}, core:{version}, singbox:{version}, panel:{version}, geodata:{version,date}}
//	files     [{path,sha256,size,mode}],压缩包里除 manifest.json 自己以外的每个文件
//
// 校验分两层(与 M6 第三节一致):压缩包整体对 SHA256SUMS;解出来的每个文件对 manifest.files。
// 本包只做纯计算,不下载、不解压;Verify 只读调用方给的那个已解包目录。
//
// 解析是严格的(DisallowUnknownFields):manifest 与解析它的后端是同一份代码产出的,
// 多出来的字段只可能是拼错或版本不匹配,早点失败比默默忽略安全。格式真有变化时抬 ManifestVersion。
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 格式常量。
const (
	Format          = "open-box-release"
	ManifestVersion = 1
	ManifestName    = "manifest.json"
	SumsName        = "SHA256SUMS"
	GeoDataName     = "geodata.json"

	// maxManifest 是 manifest.json 的读取上限。真实的一份几十 KB(文件清单),1 MiB 足够。
	maxManifest = 1 << 20
)

// Archs 是支持的架构,与 M6 第三节一致(写 amd64 / arm64,不写 x64 / aarch64)。
var Archs = []string{"amd64", "arm64"}

// ValidArch 判断架构名是否受支持。
func ValidArch(arch string) bool {
	for _, a := range Archs {
		if a == arch {
			return true
		}
	}
	return false
}

// Component 是一个组件的版本信息。geodata 多一个 date,其它组件只填 version。
type Component struct {
	Version string `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
}

// Components 是随包的各个组件。
type Components struct {
	Backend Component `json:"backend"`
	Core    Component `json:"core"`
	Singbox Component `json:"singbox"`
	Panel   Component `json:"panel"`
	Geodata Component `json:"geodata"`
}

// File 是压缩包里的一个文件。Path 相对压缩包的顶层目录,用正斜杠。
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

// Manifest 是 manifest.json 的结构。
type Manifest struct {
	Format     string     `json:"format"`
	Version    string     `json:"version"`
	Upstream   int        `json:"upstream"`
	Arch       string     `json:"arch"`
	BuiltAt    string     `json:"builtAt"`
	Components Components `json:"components"`
	Files      []File     `json:"files"`
	// Signature 是 v1 预留的签名字段:不填、不校验(M6 第八节)。留着是为了将来加签名时不用抬格式版本。
	Signature string `json:"signature,omitempty"`
}

// Parse 读一份 manifest.json 并校验。返回的 *Manifest 已经过 Validate。
func Parse(r io.Reader) (*Manifest, error) {
	dec := json.NewDecoder(io.LimitReader(r, maxManifest))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest.json 解析失败:%w", err)
	}
	// 后面还有内容说明不是一个 JSON 对象(两个对象拼在一起之类),当成损坏
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("manifest.json 后面还有多余内容")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// ParseFile 读一个 manifest.json 文件。
func ParseFile(name string) (*Manifest, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s:%w", name, err)
	}
	return m, nil
}

// Validate 检查格式、版本、架构、组件与文件清单。文件清单里的路径会按 CheckPath 逐条校验。
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("manifest.json 的 format 是 %q,应为 %q", m.Format, Format)
	}
	if m.Version == "" {
		return errors.New("manifest.json 缺 version")
	}
	if strings.ContainsAny(m.Version, "/\\ ") {
		return fmt.Errorf("manifest.json 的 version 里不能有空格或斜杠:%q", m.Version)
	}
	if m.Upstream < 0 {
		return fmt.Errorf("manifest.json 的 upstream 是负数:%d", m.Upstream)
	}
	if !ValidArch(m.Arch) {
		return fmt.Errorf("manifest.json 的 arch 是 %q,只认 %s", m.Arch, strings.Join(Archs, " / "))
	}
	if m.BuiltAt != "" {
		if _, err := time.Parse(time.RFC3339, m.BuiltAt); err != nil {
			return fmt.Errorf("manifest.json 的 builtAt 不是 RFC3339:%q", m.BuiltAt)
		}
	}
	if len(m.Files) == 0 {
		return errors.New("manifest.json 的 files 是空的")
	}
	seen := make(map[string]bool, len(m.Files))
	for i, f := range m.Files {
		if err := CheckPath(f.Path); err != nil {
			return fmt.Errorf("manifest.json 的 files[%d](%q):%w", i, f.Path, err)
		}
		if f.Path == ManifestName {
			return fmt.Errorf("manifest.json 的 files 不该列 %s 自己", ManifestName)
		}
		if seen[f.Path] {
			return fmt.Errorf("manifest.json 的 files 里 %q 出现了两次", f.Path)
		}
		seen[f.Path] = true
		if !validSHA256(f.SHA256) {
			return fmt.Errorf("manifest.json 的 files[%d](%q)的 sha256 不是 64 位小写十六进制:%q", i, f.Path, f.SHA256)
		}
		if f.Size < 0 {
			return fmt.Errorf("manifest.json 的 files[%d](%q)的 size 是负数:%d", i, f.Path, f.Size)
		}
		if !validMode(f.Mode) {
			return fmt.Errorf("manifest.json 的 files[%d](%q)的 mode 不是八进制权限:%q", i, f.Path, f.Mode)
		}
	}
	return nil
}

// File 按路径取清单里的一项。
func (m *Manifest) File(p string) (File, bool) {
	for _, f := range m.Files {
		if f.Path == p {
			return f, true
		}
	}
	return File{}, false
}

// TotalSize 是清单里所有文件的大小之和(用于升级前算磁盘空间)。
func (m *Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// Verify 校验一个已经解包的目录:清单里列的每个文件都在、大小与 sha256 都对得上,
// 而且目录里没有清单之外的文件。
//
// 目录本身可以多(空目录不带内容),但符号链接、设备文件、命名管道一律算错——
// 解包出来的东西只应该是普通文件和目录。
func (m *Manifest) Verify(root string) error {
	want := make(map[string]File, len(m.Files))
	for _, f := range m.Files {
		want[f.Path] = f
	}
	seen := make(map[string]bool, len(m.Files))
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
			return fmt.Errorf("解包目录里有不是普通文件的东西:%s", rel)
		}
		if rel == ManifestName {
			return nil
		}
		f, ok := want[rel]
		if !ok {
			return fmt.Errorf("解包目录里多出了清单之外的文件:%s", rel)
		}
		seen[rel] = true
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() != f.Size {
			return fmt.Errorf("%s 的大小是 %d,清单写的是 %d", rel, info.Size(), f.Size)
		}
		sum, err := FileSHA256(p)
		if err != nil {
			return err
		}
		if sum != f.SHA256 {
			return fmt.Errorf("%s 的 sha256 是 %s,清单写的是 %s", rel, sum, f.SHA256)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range m.Files {
		if !seen[f.Path] {
			return fmt.Errorf("清单里的 %s 在解包目录里不存在", f.Path)
		}
	}
	return nil
}

// FileSHA256 算一个文件的 sha256(小写十六进制),边读边算,不整份读进内存。
func FileSHA256(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckPath 校验清单里的相对路径:必须相对、规范、不含反斜杠与冒号,也不能跳出解包目录。
func CheckPath(p string) error {
	if p == "" {
		return errors.New("路径为空")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("路径里有 NUL")
	}
	if strings.Contains(p, "\\") {
		return errors.New("路径里不能有反斜杠")
	}
	if strings.Contains(p, ":") {
		return errors.New("路径里不能有冒号")
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("路径必须是相对路径")
	}
	// 先判 .. 再判规范:「bin/../../escape」清出来是「../escape」,报「跳出解包目录」比报「不规范」有用
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return errors.New("路径不能跳出解包目录")
		}
	}
	if path.Clean(p) != p {
		return fmt.Errorf("路径不规范,应该是 %q", path.Clean(p))
	}
	return nil
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validMode(s string) bool {
	if len(s) < 3 || len(s) > 4 {
		return false
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return false
	}
	// 只认权限位,不认 setuid / setgid / sticky:安装包里不该有这类文件
	return n <= 0o777
}

// ArchiveName 是安装包的文件名:openbox-<版本>-linux-<架构>.tar.gz。
func ArchiveName(version, arch string) string {
	return "openbox-" + version + "-linux-" + arch + ".tar.gz"
}

// TopDir 是压缩包里的顶层目录名,与文件名(去掉 .tar.gz)一致。
func TopDir(version, arch string) string {
	return "openbox-" + version + "-linux-" + arch
}

// ParseArchiveName 是 ArchiveName 的反向:从资产名认出(版本, 架构)。
// 不是安装包(例如 SHA256SUMS、install.sh)返回 ok = false。
func ParseArchiveName(name string) (version, arch string, ok bool) {
	const prefix, suffix = "openbox-", "-linux-"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tar.gz") {
		return "", "", false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tar.gz")
	i := strings.LastIndex(body, suffix)
	if i <= 0 {
		return "", "", false
	}
	version, arch = body[:i], body[i+len(suffix):]
	if !ValidArch(arch) || version == "" {
		return "", "", false
	}
	return version, arch, true
}

// Sums 是 SHA256SUMS 的内容:文件名 → 校验和。
type Sums map[string]string

// ParseSums 解析 sha256sum 格式:每行「<64 位十六进制><两个空格或「 *」><文件名>」。
// 空行和以 # 开头的行跳过。
func ParseSums(r io.Reader) (Sums, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxManifest))
	if err != nil {
		return nil, err
	}
	s := Sums{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < 66 {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行太短:%q", i+1, line)
		}
		sum := line[:64]
		if !validSHA256(sum) {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行的校验和不是 64 位小写十六进制:%q", i+1, sum)
		}
		sep := line[64:66]
		if sep != "  " && sep != " *" {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行的分隔符不对(应为两个空格或「 *」):%q", i+1, sep)
		}
		name := line[66:]
		if name == "" {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行没有文件名", i+1)
		}
		if _, dup := s[name]; dup {
			return nil, fmt.Errorf("SHA256SUMS 里 %q 出现了两次", name)
		}
		s[name] = sum
	}
	if len(s) == 0 {
		return nil, errors.New("SHA256SUMS 是空的")
	}
	return s, nil
}

// ParseSumsFile 读一个 SHA256SUMS 文件。
func ParseSumsFile(name string) (Sums, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := ParseSums(f)
	if err != nil {
		return nil, fmt.Errorf("%s:%w", name, err)
	}
	return s, nil
}

// Check 比对某个文件的校验和。got 为空(没算出来)也算错。
func (s Sums) Check(name, got string) error {
	want, ok := s[name]
	if !ok {
		return fmt.Errorf("SHA256SUMS 里没有 %q", name)
	}
	if got == "" {
		return fmt.Errorf("%q 没有算出校验和", name)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%q 的 sha256 是 %s,SHA256SUMS 写的是 %s", name, got, want)
	}
	return nil
}

// GeoData 是随包规则库的清单(geodata/geodata.json),对应 GET /update/status 的
// geoVersion / geoDate / geoCounts(M6 第三节)。
type GeoData struct {
	Version string    `json:"version"`
	Date    string    `json:"date"`
	Counts  GeoCounts `json:"counts"`
}

// GeoCounts 是规则库的分类文件个数。
type GeoCounts struct {
	Geosite int `json:"geosite"`
	Geoip   int `json:"geoip"`
}

// ParseGeoData 读一份 geodata.json。date 给空也算合法(没随包规则库时 counts 都是 0)。
func ParseGeoData(r io.Reader) (GeoData, error) {
	dec := json.NewDecoder(io.LimitReader(r, maxManifest))
	dec.DisallowUnknownFields()
	var g GeoData
	if err := dec.Decode(&g); err != nil {
		return GeoData{}, fmt.Errorf("geodata.json 解析失败:%w", err)
	}
	if g.Date != "" {
		if _, err := time.Parse("2006-01-02", g.Date); err != nil {
			return GeoData{}, fmt.Errorf("geodata.json 的 date 不是 YYYY-MM-DD:%q", g.Date)
		}
	}
	if g.Counts.Geosite < 0 || g.Counts.Geoip < 0 {
		return GeoData{}, errors.New("geodata.json 的 counts 不能是负数")
	}
	return g, nil
}
