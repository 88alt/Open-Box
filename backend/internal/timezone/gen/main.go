//go:build ignore

// 生成 zones.json:从 IANA tzdata(Debian / Ubuntu 的 /usr/share/zoneinfo)读出
//   - 时区名单:zone.tab 里的所有时区(每个国家一份,含地区名)加上 UTC;
//   - 每个时区的 POSIX 时区串:取自该时区 TZif 文件(版本 2 以上)末尾的脚注,如 Asia/Shanghai → CST-8;
//     OpenWrt 的 /etc/TZ 要的就是这个(路由器上不一定装了 tzdata);
//   - 国家 / 地区两字母代码(zone.tab 第一列);
//   - 别名:tzdata.zi 里的 L 行(L <目标> <别名>),别名换成名单里的名字用。
//
// 用法(在 backend/internal/timezone 目录里):go run gen/main.go /usr/share/zoneinfo > zones.json
// 数据是公开的 IANA 时区库,不涉及任何上游代码;更新 tzdata 后重新生成即可。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type zone struct {
	TZ      string `json:"tz"`
	Country string `json:"cc,omitempty"`
}

type out struct {
	Version string            `json:"version"`
	Zones   map[string]zone   `json:"zones"`
	Aliases map[string]string `json:"aliases"`
}

func footer(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) < 5 || string(b[:4]) != "TZif" || b[4] < '2' {
		return "", fmt.Errorf("%s 不是版本 2 以上的 TZif 文件", path)
	}
	b = bytes.TrimRight(b, "\n")
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return "", fmt.Errorf("%s 没有脚注", path)
	}
	return string(b[i+1:]), nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: go run gen/main.go <zoneinfo 目录>")
		os.Exit(2)
	}
	root := os.Args[1]
	o := out{Zones: map[string]zone{}, Aliases: map[string]string{}}

	f, err := os.Open(filepath.Join(root, "zone.tab"))
	if err != nil {
		panic(err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 {
			continue
		}
		name := cols[2]
		tz, err := footer(filepath.Join(root, name))
		if err != nil || tz == "" {
			fmt.Fprintln(os.Stderr, "跳过", name, err)
			continue
		}
		o.Zones[name] = zone{TZ: tz, Country: cols[0]}
	}
	f.Close()
	if tz, err := footer(filepath.Join(root, "UTC")); err == nil {
		o.Zones["UTC"] = zone{TZ: tz}
	} else {
		panic(err)
	}

	zi, err := os.Open(filepath.Join(root, "tzdata.zi"))
	if err != nil {
		panic(err)
	}
	sc = bufio.NewScanner(zi)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "# version ") {
			o.Version = strings.TrimSpace(strings.TrimPrefix(line, "# version "))
		}
		if strings.HasPrefix(line, "L ") {
			p := strings.Fields(line)
			if len(p) == 3 {
				o.Aliases[p[2]] = p[1]
			}
		}
	}
	zi.Close()
	// 别名链(别名 → 别名 → 名单里的名字)展开到名单里的名字;指不到名单里的丢掉
	for a, t := range o.Aliases {
		seen := 0
		for {
			if _, ok := o.Zones[t]; ok || seen > 5 {
				break
			}
			n, ok := o.Aliases[t]
			if !ok {
				break
			}
			t, seen = n, seen+1
		}
		_, targetOK := o.Zones[t]
		_, aliasIsZone := o.Zones[a] // 本身就在名单里的(如 Europe/Vatican)不算别名,按它自己的名字和国家显示
		if targetOK && a != t && !aliasIsZone {
			o.Aliases[a] = t
		} else {
			delete(o.Aliases, a)
		}
	}
	keys := make([]string, 0, len(o.Zones))
	for k := range o.Zones {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(os.Stderr, "tzdata %s:%d 个时区,%d 个别名\n", o.Version, len(keys), len(o.Aliases))
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(o); err != nil {
		panic(err)
	}
}
