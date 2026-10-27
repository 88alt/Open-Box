自研后端交接笔记(第 1 版,只管 backend;core 另见 core/HANDOFF.md)

写给下一位接手后端的 AI。读完这份,再按顺序读 backend/README.md、backend/docs/DESIGN.md、backend/STATUS.md、backend/docs/ENDPOINTS.md,
就可以接着做,不用反复问维护者。进度要看两处并对账:backend/STATUS.md 和 main 下的提交记录(git log -- backend);本文的进度是 2026-10-26 的快照。


# 零、先读这一节

- backend 是什么:自己写的 Go 后端(目录 backend/),取代上游闭源的 panel/server。前端(panel/)通过 HTTP 调用它,接口清单是 backend/docs/ENDPOINTS.md。
  目录名故意不用 panel/server:同步脚本会把 panel/server 从 main 剔除,而 backend/ 在 KEEP 里。
- 它与 core 的关系:core(openbox-core)是另一个独立项目,无状态的 stdin/stdout JSON 程序,后端用子进程方式调用它生成 sing-box 配置等。
  后端只当使用者,不改 core/;需要 core 新增或改动方法时,在 STATUS.md 里记一条"需要 core 配合",由 core 那边按 core/HANDOFF.md 另做。
- 不带激活机制:前端残留的 /api/openbox/activation* 只返回固定的"无需激活"状态。
- 工作单元:STATUS.md 里的一个条目 = 一个提交 = 实现加测试加打勾加日志。
- 沙箱推不了仓库:每做完一个条目出一个 .patch(基于最新 origin/main),维护者在 Git Bash 里应用并推送(第七节)。
- 沙箱里没有路由器:标"需真机"的条目,代码和单元测试做完后打 [~],由维护者真机验证后改 [x]。

接手后先做:
1. 克隆 https://github.com/timeflysoon/Open-Box (公开),git checkout main。
2. 先对账:git fetch 后把 git log --oneline origin/main -- backend 和 STATUS.md 对照着看。提交里已经做了、STATUS.md 没打勾的,先单独补打勾和日志,
   不要重复实现、重复推送;STATUS.md 打了勾、找不到对应提交的,当作未完成。对完账再取第一个没打勾的条目。每次出补丁前再 git fetch 一次,确认 origin/main 没有新提交。
3. 沙箱里确认 go version(缺 Go 用 apt 装);沙箱访问不了 Go 模块代理,优先用标准库;需要 sing-box 的测试在没有可执行文件时 t.Skip。
4. 做完一个条目:go vet ./... 和 go test ./... 全绿,STATUS.md 打勾并加日志——**和代码放在同一个提交、同一个补丁里,不要拆开,也不要留到最后补**
   (额度可能随时用完,拆开就会出现代码已交付、清单没更新的漂移);提交说明 "backend: <阶段> <做了什么>"。


# 一、已定决策(详细见 docs/DESIGN.md 第二节)

Go 1.25.12 静态编译;模块名 github.com/timeflysoon/Open-Box/backend;接口契约就是 ENDPOINTS.md 加前端 TypeScript 类型;
第一阶段用子进程方式调 core;不校验 hello 的 buildID;数据先用 JSON 文件加原子写;认证用密码哈希加会话 Cookie 加登录限速;
前端静态文件由后端提供;订阅"登录"来源暂返回 501;教程视频返回空列表;默认监听 0.0.0.0:3036;只依赖标准库和少量成熟库,引入依赖要在提交说明里写理由。
"变更安全"是最重要的一条:任何改 DNS、防火墙、路由、sing-box 启停的操作都要先校验再应用、可回滚,无法确认系统状态时返回错误而不是猜(DESIGN 第四节)。


# 二、仓库与同步(只列与 backend 有关的)

- 自研只在 main;catch-up-upstream 纯拉取上游,是上游的备份。
- main 的内容每次由维护者本机的 sync-main.sh 重新生成:除 panel/ 和 KEEP 目录外都被上游版本覆盖。backend/ 在 KEEP 里,不会被覆盖。
  KEEP 的目标内容:core、backend、.github/workflows、scripts;实际以维护者本机的 sync-main.sh 为准。KEEP 只能写目录。
- 自己的东西放进这些目录,放在别处会在下次同步时丢失。根目录的 .gitignore、README.md 等会被上游覆盖,忽略规则写在 backend/.gitignore 里。
- 后端有独立的 Actions 工作流(vet、test、编译 amd64 / arm64),不动 release.yml(它是上游版本,会被同步覆盖)。
- 过渡期的发布版本号规则 v0.1.<上游号>-main.<n>,正式发布前由维护者重定;main 上不要随便跑完整的 Release(会建标签并发布)。


# 三、进度标记(2026-10-26 快照;细项以 STATUS.md 为准)

图例:[x] 完成  [~] 代码完成、待真机或待云端确认  [ ] 未做

[x] M0 骨架:HTTP 服务、日志、原子写存储、静态文件、鉴权(auth/*)、storage 与 background-image、Actions 首次运行全绿
[~] M1 配置流水线:已完成 core 客户端、profile 与 defaults、订阅(拉取 / 解析 / 增删改查 / 排序 / 刷新 / 预览 / 改名规则 / Clash 格式)、
    groups、rulesets 的 preview 与 import、config/preview(配置生成)、节点测速(nodes/latency、chain-proxies/latency)。
    配置生成与校验(internal/singbox)的 Check 只用假的 sing-box 脚本测过,需真机用真实内核验证。
[ ] M1 未完成:policies/entries、rulesets entries 与 refresh、原子替换 → 健康检查 → 回滚、生成配置引用的本地规则集文件落盘、service、kernel、真机联调
[ ] M2 DNS 与过滤;M3 观测;M4 自动化(订阅定时更新、failover);M5 系统运维(backup、deploy、factory-reset、update 等);M6 打包与发布


# 四、后续工作(按顺序)

1. 先做 M1 的部署部分:本地规则集文件落盘、原子替换加健康检查加回滚、service、kernel。没有它们,生成的配置跑不起来。
   落盘必须先于部署:没有规则集文件,sing-box check 会因文件不存在而失败。
2. M1 真机联调:添加订阅 → 生成配置 → sing-box 启动成功(维护者做)。
3. M2 到 M5 按 STATUS.md 的顺序。
4. M6 打包与发布:编译 amd64 / arm64 的后端,打包 panel/dist、openbox-core、sing-box 内核、安装与卸载脚本;与 main 的发布流程对接。
5. 清理旧文件 scripts/native-arm64/:真机联调通过之后,单独一个提交。
6. 前端和 ENDPOINTS.md 对不上时,先改 ENDPOINTS.md 再改实现,同一个提交里完成。


# 五、"跟随上游"怎么做(范围写得很窄)

维护者希望后端也能跟着上游的新功能走。可以做的:
- 前端是规格:自己的前端(panel/src)要什么接口、什么字段,后端就实现什么;前端改了,ENDPOINTS.md 和后端同一个提交里跟着改。
- sing-box 官方文档和发布说明:配置和行为以官方为准。
- 上游公开发布的说明性文字(Release 说明、README、面向用户的功能介绍):只用来了解"新增了什么功能",再自己设计实现。
- OpenWrt / Debian 官方文档、协议规范。
不做:读取、复制、反编译上游的闭源后端和核心(panel/server/index.mjs、panel/server/native/*、v0.1.321 之后的任何上游编译产物);为兼容上游服务端而保持其校验;任何授权 / 激活绕过。
需要新功能时:在 STATUS.md 加条目,写清楚用户可见的行为,自己设计、自己测试,选保守、可回滚的做法,并在提交说明里写明这是自己的设计。


# 六、来源与合规

1. 上游自 v0.1.321 起随包附带专有许可:Open-Box 自有程序(服务端、原生核心)为闭源专有软件,禁止再分发和重新打包、反编译和逆向恢复实现、绕过授权检查。
2. tag engine-snapshot(上游 v0.1.318 快照)里有可读的旧版 Node 服务端。我检查过该快照的许可文件:只有 Node.js 和 sing-box 的许可,没有 Open-Box 自己的许可文本,所以使用条件不明确。
   在维护者明确确认之前,把它当作不可用:不要复制其中的代码,也不要照着它逐行改写。
3. 前端里有几处是照着上游新版的编译产物对齐出来的(订阅"登录"来源、教程视频等)。对应接口在后端按默认处理(第一节),不要为它们去研究上游行为。
4. 这些判断由维护者决定;接手的 AI 不要自己放宽。


# 七、与维护者协作的约定

1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 补丁交付:克隆公开库 → git checkout main → 改 → git commit → git format-patch -1 → 另一份刚克隆的 main 上 git apply --check、git apply,
   跑 go vet ./... 和 go test ./... 全绿才交。出补丁前先 git fetch,main 前进了就在最新 main 上重做。
   维护者把补丁下载到 E:\Projects,在 Git Bash 里应用(git am,或 git apply 加手动提交)并推送。每次回复里给完整命令块。提交说明要和补丁内容对得上。
3. 要读他本机的文件:给 PowerShell 脚本,把内容写进剪贴板,他直接粘贴回来;不要在磁盘上生成文件(保护固态硬盘);
   让他贴回来的输出尽量短。他的 PowerShell 不支持 &&,也没有 grep、awk;需要这类命令放 Git Bash。
4. 前端联调:在 panel/ 下 ZASHBOARD_DEV_PROXY_TARGET=http://<路由器IP>:3036 npm run dev,把前端的 /api 代理到真实后端。
5. 云端验证走 GitHub Actions。交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。
6. 沙箱的坑:sh 不支持花括号展开,用 bash;cut -c 会切断多字节中文;输出含二进制或超长单行时终端会报编码错误,用 Python 读再打印;
   换会话后沙箱里的克隆、Go 环境都会丢,要重新准备;沙箱里 git pull 遇分叉会拒绝,用 git fetch 加 git reset --hard origin/main(只影响沙箱本地)。


# 八、验证方法

- 表驱动单元测试;处理函数用 httptest;会改系统的代码放 internal/system,通过接口抽象,单元测试用假实现。
- 需要 sing-box 的测试在没有可执行文件时 t.Skip;云端工作流里装好固定版本后跑(现为 1.14.4)。
- 真机验证由维护者做;你在 STATUS.md 里标 [~],并在日志里写清楚他要做什么、怎么判断成功。
- 所有改动 go vet ./... 和 go test ./... 全绿才交补丁。


# 九、提交记录(backend,新的在最上面;完整以 git log -- backend 为准)

5aa6e21 M1 节点测速;244daf3 M1 配置生成与 sing-box check;057fd18 M1 rulesets preview / import;05fda53 M1 groups;b68287c M1 订阅改名规则;
40dcca8 M1 Clash 格式解析;9a27dff M1 订阅接口;a978177 M1 订阅拉取与解析;e325a79 M1 profile 与 defaults;0684316 M1 core 客户端;
9b32137 M0 确认云端 Actions 首次全绿;1665127 M0 Actions 工作流;46d9571 M0 storage;775da48 M0 auth;292f024 M0 静态文件;73c7491 M0 存储;
4542ff3 M0 HTTP 服务;8782bcc M0 go.mod 与入口;3ffa443 设计文档、接口清单、进度清单与交接规则
