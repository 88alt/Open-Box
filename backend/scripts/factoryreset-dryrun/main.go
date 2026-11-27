// factory-reset 的「只列出会删哪些文件」预演(dry-run)。
//
// 为什么单独写一个:STATUS 里这一条要真机验证,但真机执行恢复出厂会把数据全清掉。
// 维护者要求先在临时环境预演、看清楚会删什么,再决定是否真机执行。
//
// 这个程序不调用 factoryreset.Service(它会真的删),而是按 main.go 里 factoryWipes 的**同一份清单**
// 逐项判断「这个路径在预演目录里会不会被删」,只打印结论。
// 清单与 main.go 不一致时,以 main.go 为准,改这里的同时要改那边。
//
// 用法(在 backend/ 下):
//
//	go run ./scripts/factoryreset-dryrun -data <数据目录> -config <配置目录>
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// keepInStore 与 main.go 里 st.Clear("auth", "dns-takeover", "terminal-net") 的保留名单一致。
// auth 也在这里:状态清除那一步不删它,访问密码由最后的 Final 步(authMgr.Reset)单独清。
var keepInStore = map[string]bool{"auth": true, "dns-takeover": true, "terminal-net": true}

type verdict struct {
	path   string
	action string // 删除 / 清空目录内容 / 保留
	reason string
}

func main() {
	dataDir := flag.String("data", "", "数据目录(预演用,不会被修改)")
	configDir := flag.String("config", "", "配置目录")
	flag.Parse()
	if *dataDir == "" || *configDir == "" {
		fmt.Fprintln(os.Stderr, "需要 -data 和 -config")
		os.Exit(2)
	}

	var vs []verdict
	deleted := map[string]bool{}

	// 1) 明确删单个文件(对应 RemoveFiles 的几项,外加 failover.json)
	removeFiles := []struct{ path, reason string }{
		{filepath.Join(*configDir, "failover.json"), "故障转移计划"},
		{filepath.Join(*configDir, "config.json"), "生成的内核配置"},
		{filepath.Join(*configDir, "config.prev.json"), "生成的内核配置(上一版备份;Deployer.PrevPath 生成)"},
		{filepath.Join(*dataDir, "cache.db"), "内核缓存"},
	}
	for _, f := range removeFiles {
		if _, err := os.Stat(f.path); err == nil {
			vs = append(vs, verdict{f.path, "删除", f.reason})
			deleted[f.path] = true
		}
	}

	// 2) 清空目录内容(目录本身留着):rules / dns-filter / content-filter
	clearDirs := []struct{ dir, reason string }{
		{filepath.Join(*dataDir, "rules"), "规则集"},
		{filepath.Join(*dataDir, "dns-filter"), "过滤名单"},
		{filepath.Join(*dataDir, "content-filter"), "内容过滤文本"},
	}
	for _, d := range clearDirs {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(d.dir, e.Name())
			vs = append(vs, verdict{p, "删除(目录保留)", d.reason})
			deleted[p] = true
		}
	}

	// 3) 状态目录里的 .json:除保留名单外全删(对应 store.Clear 的规则)
	stateDir := filepath.Join(*dataDir, "state")
	if entries, err := os.ReadDir(stateDir); err == nil {
		for _, e := range entries {
			fn := e.Name()
			if e.IsDir() || !strings.HasSuffix(fn, ".json") {
				vs = append(vs, verdict{filepath.Join(stateDir, fn), "保留", "不是 .json 状态文件"})
				continue
			}
			name := strings.TrimSuffix(fn, ".json")
			if keepInStore[name] {
				why := "撤销用的记录,恢复出厂不删"
				if name == "auth" {
					why = "访问密码:本步不删,由最后的 Final 步(authMgr.Reset)单独清"
				}
				vs = append(vs, verdict{filepath.Join(stateDir, fn), "保留", why})
				continue
			}
			p := filepath.Join(stateDir, fn)
			vs = append(vs, verdict{p, "删除", "已保存的数据"})
			deleted[p] = true
		}
	}

	// 4) 明确保留的(certs、dns-takeover、自启设置)
	keepDirs := []string{filepath.Join(*configDir, "certs")}
	for _, d := range keepDirs {
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() && !deleted[p] {
				vs = append(vs, verdict{p, "保留", "证书,恢复出厂不删"})
			}
			return nil
		})
	}

	// 5) 预演目录里所有文件最终归属:清单没提到的也列出来,避免「以为删了其实没删」
	var all []string
	_ = filepath.Walk(*dataDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			all = append(all, p)
		}
		return nil
	})
	_ = filepath.Walk(*configDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			all = append(all, p)
		}
		return nil
	})
	seen := map[string]bool{}
	for _, v := range vs {
		seen[v.path] = true
	}
	for _, p := range all {
		if !seen[p] {
			vs = append(vs, verdict{p, "保留", "不在恢复出厂的清单里"})
		}
	}

	sort.Slice(vs, func(i, j int) bool { return vs[i].path < vs[j].path })

	var del, keep int
	fmt.Println("恢复出厂设置预演(不会删除任何文件)")
	fmt.Println(strings.Repeat("-", 72))
	for _, v := range vs {
		fmt.Printf("%-16s %s\n", v.action, filepath.ToSlash(v.path))
		fmt.Printf("%-16s   ↳ %s\n", "", v.reason)
		if strings.HasPrefix(v.action, "删除") {
			del++
		} else {
			keep++
		}
	}
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("合计:会删 %d 个,保留 %d 个\n", del, keep)
}
