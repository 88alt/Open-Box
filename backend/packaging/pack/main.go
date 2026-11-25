// Command pack 把一个已经摆好的安装目录打成发布包(M6 第三节 / 第六节 e)。
//
// 用法(在 backend/ 下):
//
//	go run ./packaging/pack -root <暂存目录> -version v0.1.484-main.1 -arch amd64 \
//	    -upstream 484 -out <输出目录> [-singbox-version 1.14.4-openbox-60] \
//	    [-core-version <core buildID>] [-geodata-version <提交> -geodata-date 2026-11-25]
//
// 干三件事:写 manifest.json(逐文件 sha256 / 大小 / 权限)、打 tar.gz(模式按清单写,
// 不读磁盘权限,Windows 上 chmod 无效也不会打出没有执行位的 bin/*)、写 SHA256SUMS。
// 组件的 core 版本默认从 <root>/bin/core-build.json 读,其它默认取 -version。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "打包失败:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	root := fs.String("root", "", "暂存目录(里面已经摆好 bin/、panel/、etc/ …)")
	version := fs.String("version", "", "发布版本号,如 v0.1.484-main.1")
	arch := fs.String("arch", "", "amd64 / arm64")
	upstream := fs.Int("upstream", 0, "上游号,如 484")
	out := fs.String("out", "", "输出目录(放 tar.gz 与 SHA256SUMS)")
	builtAt := fs.String("built-at", "", "构建时间(RFC3339),默认现在(UTC)")
	coreVersion := fs.String("core-version", "", "openbox-core 的版本串,默认读 <root>/bin/core-build.json 的 buildID")
	singboxVersion := fs.String("singbox-version", "", "sing-box 的版本串,如 1.14.4-openbox-60")
	panelVersion := fs.String("panel-version", "", "面板版本串,默认取 -version")
	geodataVersion := fs.String("geodata-version", "", "随包规则库的提交,不随包时留空")
	geodataDate := fs.String("geodata-date", "", "随包规则库的日期 YYYY-MM-DD")
	modesFile := fs.String("modes", "", "权限表(每行「<八进制权限> <路径>」),不给就按内置规则")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" || *version == "" || *arch == "" || *out == "" {
		return fmt.Errorf("必须给 -root / -version / -arch / -out")
	}
	if !release.ValidArch(*arch) {
		return fmt.Errorf("-arch 只认 amd64 / arm64,得到 %q", *arch)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if *builtAt != "" {
		t, err := time.Parse(time.RFC3339, *builtAt)
		if err != nil {
			return fmt.Errorf("-built-at 不是 RFC3339:%w", err)
		}
		when = t
	}

	coreVer := *coreVersion
	if coreVer == "" {
		v, err := coreBuildID(*root)
		if err != nil {
			return err
		}
		coreVer = v
	}
	panelVer := *panelVersion
	if panelVer == "" {
		panelVer = *version
	}

	m := &release.Manifest{
		Format:   release.Format,
		Version:  *version,
		Upstream: *upstream,
		Arch:     *arch,
		BuiltAt:  when.Format(time.RFC3339),
		Components: release.Components{
			Backend: release.Component{Version: *version},
			Core:    release.Component{Version: coreVer},
			Singbox: release.Component{Version: *singboxVersion},
			Panel:   release.Component{Version: panelVer},
			Geodata: release.Component{Version: *geodataVersion, Date: *geodataDate},
		},
	}
	// 随包规则库:先写 geodata.json,再写清单(它要进 files 列表,后端的 /update/status 从这里取版本与条数)
	if *geodataVersion != "" {
		geoDir := filepath.Join(*root, "geodata")
		counts, err := release.CountGeoFiles(geoDir)
		if err != nil {
			return fmt.Errorf("读规则库目录失败:%w", err)
		}
		if counts.Geosite == 0 && counts.Geoip == 0 {
			return fmt.Errorf("%s 里没有 .srs,不该带 -geodata-version", geoDir)
		}
		g := release.GeoData{Version: *geodataVersion, Date: *geodataDate, Counts: counts}
		if err := release.WriteGeoData(geoDir, g); err != nil {
			return fmt.Errorf("写 %s 失败:%w", release.GeoDataName, err)
		}
		fmt.Printf("规则库:%s(%d 个 geosite / %d 个 geoip)\n", g.Version, counts.Geosite, counts.Geoip)
	}

	modeOf := releaseMode
	if *modesFile != "" {
		table, err := readModes(*modesFile)
		if err != nil {
			return err
		}
		modeOf = func(path string) string {
			if v, ok := table[path]; ok {
				return v
			}
			return releaseMode(path)
		}
	}
	if err := release.WriteManifestWith(*root, m, modeOf); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	name := release.ArchiveName(*version, *arch)
	archive := filepath.Join(*out, name)
	if err := release.CreateArchive(*root, archive, m, release.TopDir(*version, *arch), when); err != nil {
		return err
	}
	if err := release.WriteSumsFile(filepath.Join(*out, release.SumsName), map[string]string{name: archive}); err != nil {
		return err
	}
	fmt.Printf("打包完成:%s\n  %d 个文件,解压后 %d 字节\n  组件:%+v\n", archive, len(m.Files), m.TotalSize(), m.Components)
	return nil
}

// readModes 读权限表:每行「<八进制权限> <路径>」,# 开头的行跳过。
// build-package.sh 用 git 里记的权限生成这张表(openwrt/ debian/ 是同步来的,权限以 git 为准),
// 这样打包不依赖磁盘上的权限位。
func readModes(name string) (map[string]string, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("读不到权限表 %s:%w", name, err)
	}
	table := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s 第 %d 行格式不对:%q", name, i+1, line)
		}
		table[fields[1]] = fields[0]
	}
	return table, nil
}

// releaseMode 是随包文件的权限规则:该执行的 0755,其余 0644。
// 不读磁盘权限——Windows / MSYS 上 chmod 是空操作,读出来一律 0666,打出来的包 bin/* 没法执行。
func releaseMode(path string) string {
	switch {
	case path == "bin/core-build.json":
		return "0644"
	case strings.HasPrefix(path, "bin/"),
		strings.HasPrefix(path, "openwrt/initd/"),
		strings.HasPrefix(path, "openwrt/bin/"),
		strings.HasPrefix(path, "debian/bin/"),
		strings.HasSuffix(path, ".sh"):
		return "0755"
	case path == "service/openwrt/openbox-backend":
		return "0755"
	}
	return "0644"
}

// coreBuildID 读 build-core.sh 写在 core 旁边的 core-build.json。
func coreBuildID(root string) (string, error) {
	name := filepath.Join(root, "bin", "core-build.json")
	data, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("读不到 %s(要用 -core-version 指定):%w", name, err)
	}
	var v struct {
		BuildID string `json:"buildID"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("%s 解析失败:%w", name, err)
	}
	if v.BuildID == "" {
		return "", fmt.Errorf("%s 里没有 buildID", name)
	}
	return v.BuildID, nil
}
