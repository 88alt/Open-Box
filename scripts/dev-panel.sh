#!/usr/bin/env bash
# Open-Box 本地开发:构建前端 + 构建 Go 后端 + (重)启动 + 健康检查
# main 上没有 panel/server(Node 服务端),面板由 backend/(Go)提供,所以这里起的是 openbox-backend。
# 用法:bash scripts/dev-panel.sh
# 环境变量:PORT(默认 3036)、SKIP_PANEL_BUILD=1(跳过前端构建,沿用现有 panel/dist)
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
panel_dir="$repo_root/panel"
backend_dir="$repo_root/backend"
pid_file="$repo_root/.dev-panel.pid"
log_file="$repo_root/.dev-panel.log"
data_root="$repo_root/.dev-data"
port="${PORT:-3036}"

# 旧脚本写死了 macOS 的 node 路径;只有目录存在才加进 PATH
mac_node="$HOME/.local/share/node-v24.18.0-darwin-arm64/bin"
[[ -d "$mac_node" ]] && export PATH="$mac_node:$PATH"

exe=""
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) exe=".exe" ;; esac
bin="$repo_root/.dev-backend$exe"

# 传给原生 Windows 程序的路径要用 C:/... 形式,不能是 /e/...
native() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi
}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "FAIL: 找不到 $1。$2" >&2; exit 1; }
}
need node "请先安装 Node.js(panel/package.json 要求的版本)"
need go "请先安装 Go(backend/go.mod 要求 1.25 及以上)"
need curl "Git Bash 自带 curl,检查 PATH"

if command -v pnpm >/dev/null 2>&1; then
  pnpm_cmd=(pnpm)
else
  need corepack "没有 pnpm,也没有 corepack"
  pnpm_cmd=(corepack pnpm)
fi

stop_old() {
  [[ -f "$pid_file" ]] || return 0
  local pid
  pid="$(cat "$pid_file" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.5
    done
    # Git Bash 下原生 exe 偶尔 kill 不掉,退一步用 taskkill
    if kill -0 "$pid" 2>/dev/null && command -v taskkill >/dev/null 2>&1; then
      local winpid
      winpid="$(cat "/proc/$pid/winpid" 2>/dev/null || echo "$pid")"
      taskkill //F //PID "$winpid" >/dev/null 2>&1 || true
    fi
  fi
  rm -f "$pid_file"
}

if [[ "${SKIP_PANEL_BUILD:-0}" != "1" ]]; then
  cd "$panel_dir"
  [[ -d node_modules ]] || "${pnpm_cmd[@]}" install
  "${pnpm_cmd[@]}" run build
fi
[[ -f "$panel_dir/dist/index.html" ]] || { echo "FAIL: 没有 panel/dist/index.html,前端没构建出来" >&2; exit 1; }

# 先停旧进程再编译:Windows 上正在运行的 exe 不能被覆盖
stop_old
(cd "$backend_dir" && go build -o "$bin" ./cmd/openbox-backend)

mkdir -p "$data_root/data" "$data_root/config"
nohup "$bin" \
  -listen "127.0.0.1:$port" \
  -data-dir "$(native "$data_root/data")" \
  -config-dir "$(native "$data_root/config")" \
  -web-dir "$(native "$panel_dir/dist")" \
  -dns-query-log off \
  >"$log_file" 2>&1 &
echo $! >"$pid_file"

for _ in $(seq 1 40); do
  if curl -sf --max-time 2 "http://127.0.0.1:$port/api/health" >/dev/null; then
    echo "Open-Box panel OK: http://127.0.0.1:$port"
    exit 0
  fi
  if ! kill -0 "$(cat "$pid_file")" 2>/dev/null; then
    echo "FAIL: 后端进程已退出,日志末尾:" >&2
    tail -n 20 "$log_file" >&2 || true
    exit 1
  fi
  sleep 0.5
done
echo "FAIL: panel 未通过健康检查,日志见 .dev-panel.log" >&2
tail -n 20 "$log_file" >&2 || true
exit 1
