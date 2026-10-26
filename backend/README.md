# backend/ — 自研后端(取代上游的 panel/server)

面板前端(`panel/`)通过 HTTP 调用的所有 `/api/...` 接口,由这里实现。目标是**自己写、自己维护**,不依赖也不打包上游的闭源后端。
目录名故意不用 `panel/server`:同步脚本会把 `panel/server` 从 main 里剔除,而 `backend/` 在 `sync-main.sh` 的 `KEEP` 里,不会被上游覆盖。

## 接手前先读(按顺序)
1. 本文件:规则和工作方式。
2. `docs/DESIGN.md`:架构、已定的决定、分阶段计划、每阶段的验收标准。
3. `STATUS.md`:进度清单。**从第一个没打勾的条目开始做**,不要跳阶段。
4. `docs/ENDPOINTS.md`:前端调用的全部接口(方法、路径、前端函数、类型位置)。这是接口契约。

## 怎么接着做(不用再问维护者)
1. **先对账,再动手**:`git fetch` 后把 `git log --oneline origin/main -- backend/`(必要时加 `--stat`)和 `STATUS.md` 对照着看。
   - 提交里已经做了、`STATUS.md` 却没打勾的:先补打勾和日志(单独一个提交 `backend: 补记进度 <条目>`),**不要重复实现、重复推送**。
   - `STATUS.md` 打了勾、却找不到对应提交的:当作未完成。
   - 每次出补丁前再 `git fetch` 一次,看 `origin/main` 有没有新提交,避免和别人重复或漏掉。
2. 取第一个未完成条目,按 `docs/DESIGN.md` 里该阶段的约定实现,**同时写测试**(没有测试的条目不算完成)。
3. 完成后:在 `STATUS.md` 打勾并在「日志」里加一行(日期、条目、一句话);**打勾和日志必须与代码放在同一个提交、同一个补丁里,不要拆开,
   也不要留到最后再补**(额度或时间可能随时用完,拆开就会出现代码已交付、清单没更新的漂移);提交说明格式 `backend: <阶段> <做了什么>`,
   例如 `backend: M1 订阅 CRUD 与预览接口`。一次提交只做一个条目。
4. 遇到 `docs/DESIGN.md` 末尾「待维护者确认」里列的事项,**直接按其中写的默认做法继续**,并在提交说明里写明用了默认值;
   只有默认做法行不通(例如会破坏路由器联网)才停下来问。
5. 发现前端的接口调用和 `docs/ENDPOINTS.md` 对不上(漏了、多了、类型变了):先改 `docs/ENDPOINTS.md`,再改实现,同一个提交里完成。

## 硬性规则(不能违反)
- **只依据这些来源写代码**:前端源码(`panel/src`,尤其 `api/openbox.ts`、`api/clientApp.ts`)、本仓库自研的 `core/`、
  sing-box / OpenWrt / Debian 的公开文档、协议规范。
- **不要读取、复制、反编译上游的闭源部分**:`panel/server/index.mjs`、`panel/server/native/*`、任何 v0.1.321 之后的上游编译产物。
  `main` 里没有它们,也不要从 `catch-up-upstream` 分支取来参考。v0.1.318 快照(tag `engine-snapshot`)只能在维护者确认其许可条件之后,
  才可作为行为参考,默认当作**不可用**;`docs/DESIGN.md` 里有说明。
- **后端没有「激活 / 授权」概念**,也不写任何授权桩。前端残留的 `/api/openbox/activation*` 调用只返回固定的「无需激活」状态(见 DESIGN)。
- **不要弄断路由器的网络**:任何会改 DNS、防火墙、路由、sing-box 启停的操作,都必须可回滚(见 DESIGN「变更安全」)。
- 不要改 `panel/server/`、不要往 `backend/` 之外的上游同步目录(`openwrt/`、`debian/`、`docker/`、`docs/` 等)里放自己的改动,它们会被同步覆盖。

## 维护者的工作方式(接手的 AI 请遵守)
- 维护者在 Windows 上用 Git Bash;**交付改动用 `.patch` 文件**,他下载到 `E:\Projects`,在 Git Bash 里 `git am` 应用并推送。补丁基于最新的 `origin/main`。
- 需要读他本机的文件时,给一段 **PowerShell 脚本,把内容写进剪贴板**,他直接粘贴回来;**不要让他在磁盘上生成文件**(保护固态硬盘),
  要他贴回来的输出尽量短(只取必要的行)。
- 云端验证走 GitHub Actions(沙盒里没有路由器,也可能没有 Go)。
- 前端联调:在 `panel/` 下 `ZASHBOARD_DEV_PROXY_TARGET=http://<路由器IP>:<端口> npm run dev`,把前端的 `/api` 代理到真实后端。
- 沟通用中文,简洁,先说结论。

## 目录(目标结构,随进度创建)
```
backend/
  README.md  STATUS.md  .gitignore
  go.mod                      module github.com/timeflysoon/Open-Box/backend
  cmd/openbox-backend/        入口
  internal/
    server/   HTTP 路由、中间件、静态文件、会话
    api/      按接口分组的处理函数(与 docs/ENDPOINTS.md 的分组一一对应)
    store/    持久化(原子写)
    core/     openbox-core 子进程客户端(stdin/stdout JSON 协议)
    singbox/  配置落盘、sing-box check / 启停 / 热重载
    sub/      订阅拉取、解析、定时更新
    system/   DNS 接管、防火墙、dnsmasq、服务控制、时区等
    probe/    测速、站点延迟、流量采集
  docs/       DESIGN.md  ENDPOINTS.md
```
