#!/bin/sh
# Open-Box 安装 / 升级脚本(自研后端)。设计见 backend/docs/M6.md 第五节。
#
# 用法:
#   curl -fsSL .../install.sh | sh
#   sh install.sh [--version vX] [--mirror <前缀>] [--file <本地包>] [--port <端口>]
#                 [--force] [--no-start] [--root <假根目录>]
#
# 做什么:认平台与架构 → 取安装包 → 校验(SHA256SUMS + 逐文件对 manifest)→ 查冲突 →
# 放文件 → 写服务脚本 → 启用并只启动后端。
#
# 不做什么:不启动内核、不接管 DNS、不改防火墙——这些都要用户在面板里点了才发生(M6 第五节第 6 条)。
#
# 幂等:重复运行是「升级式覆盖」——二进制、前端、规则库、服务脚本覆盖;数据目录与配置目录**一律不动**。
#
# --root 只给测试用(假根目录):所有路径都前缀到它,服务管理器的调用全部跳过。
set -eu

PROG=install.sh
die() { echo "[$PROG] 错误:$*" >&2; exit 1; }
info() { echo "[$PROG] $*" >&2; }

ROOT=/
VERSION=""
MIRROR=""
FILE=""
PORT=""
FORCE=0
START=1
REPO_SLUG=timeflysoon/Open-Box

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || die "--version 后面要跟版本号"; VERSION=$2; shift 2 ;;
    --mirror) [ "$#" -ge 2 ] || die "--mirror 后面要跟前缀"; MIRROR=$2; shift 2 ;;
    --file) [ "$#" -ge 2 ] || die "--file 后面要跟路径"; FILE=$2; shift 2 ;;
    --port) [ "$#" -ge 2 ] || die "--port 后面要跟端口"; PORT=$2; shift 2 ;;
    --root) [ "$#" -ge 2 ] || die "--root 后面要跟目录"; ROOT=$2; shift 2 ;;
    --force) FORCE=1; shift ;;
    --no-start) START=0; shift ;;
    -h|--help) sed -n '2,20p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "不认识的参数:$1" ;;
  esac
done

case "$ROOT" in
  */) ROOT=${ROOT%/} ;;
esac
[ -z "$ROOT" ] && ROOT=/
# p <绝对路径>:前缀假根目录
p() { printf '%s%s' "$ROOT" "$1"; }

REAL_ROOT=0
[ "$ROOT" = / ] && REAL_ROOT=1

# ---------- 1. 平台与架构 ----------

if [ -f "$(p /etc/openwrt_release)" ]; then
  PLATFORM=openwrt
elif [ -f "$(p /etc/os-release)" ] && grep -qiE '^(ID|ID_LIKE)=.*(debian|ubuntu)' "$(p /etc/os-release)"; then
  PLATFORM=debian
else
  die "只支持 OpenWrt 与 Debian / Ubuntu(没找到 /etc/openwrt_release,os-release 里也不是 debian 系)"
fi

MACHINE=$(uname -m)
case "$MACHINE" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构:$MACHINE(只支持 x86_64 / aarch64)" ;;
esac

info "平台 $PLATFORM,架构 $ARCH"

# ---------- 工具 ----------

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | sed 's/.*= *//'
  else
    die "找不到 sha256sum / shasum / openssl,没法校验下载的文件"
  fi
}

# fetch <url> <目标文件>
fetch() {
  _url=$1
  _dest=$2
  [ -n "$MIRROR" ] && _url="$MIRROR$_url"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 20 -o "$_dest" "$_url" || die "下载失败:$_url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$_dest" "$_url" || die "下载失败:$_url"
  else
    die "找不到 curl / wget,没法下载 $_url"
  fi
}

TMP=$(mktemp -d 2>/dev/null || mktemp -d -t openbox-install)
# 统一成 POSIX 路径:MSYS / Cygwin 上 mktemp 会给出 C://... 混着正斜杠,后面拼给 tar 会打不开
TMP=$(cd "$TMP" && pwd)
cleanup_tmp() { ( cd "$TMP" && rm -rf ./* ) 2>/dev/null || true; rmdir "$TMP" 2>/dev/null || true; }
trap cleanup_tmp EXIT INT TERM

# ---------- 2. 取安装包 ----------

if [ -n "$FILE" ]; then
  [ -f "$FILE" ] || die "找不到安装包:$FILE"
  TARBALL=$FILE
  info "用本地安装包:$TARBALL"
  if [ -f "$FILE.sha256" ]; then
    WANT=$(cut -d' ' -f1 "$FILE.sha256")
    GOT=$(sha256_of "$TARBALL")
    [ "$WANT" = "$GOT" ] || die "校验和不符:$GOT,应为 $WANT"
  else
    info "没有 $FILE.sha256,跳过压缩包整体校验(解包后仍逐文件核对 manifest)"
  fi
  TAG=""
else
  if [ -z "$VERSION" ]; then
    info "查最新版本"
    fetch "https://api.github.com/repos/$REPO_SLUG/releases/latest" "$TMP/latest.json"
    VERSION=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP/latest.json" | head -n 1)
    [ -n "$VERSION" ] || die "没查到最新版本(网络或 GitHub API 不通),请用 --version 指定"
  fi
  TAG=$VERSION
  NAME="openbox-$VERSION-linux-$ARCH.tar.gz"
  BASE="https://github.com/$REPO_SLUG/releases/download/$TAG"
  info "下载 $NAME"
  fetch "$BASE/$NAME" "$TMP/$NAME"
  fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
  WANT=$(sed -n "s/^\([0-9a-f]\{64\}\)[ *][ *]*$NAME\$/\1/p" "$TMP/SHA256SUMS")
  [ -n "$WANT" ] || die "SHA256SUMS 里没有 $NAME"
  GOT=$(sha256_of "$TMP/$NAME")
  [ "$WANT" = "$GOT" ] || die "压缩包校验和不符:$GOT,应为 $WANT"
  TARBALL="$TMP/$NAME"
fi

info "解包"
mkdir -p "$TMP/unpack"
tar xzf "$TARBALL" -C "$TMP/unpack" || die "解包失败"
TOP=$(cd "$TMP/unpack" && ls -1 | head -n 1)
[ -d "$TMP/unpack/$TOP" ] || die "压缩包里没有顶层目录"
SRC="$TMP/unpack/$TOP"
[ -f "$SRC/manifest.json" ] || die "压缩包里没有 manifest.json"

# ---------- 3. 逐文件核对 manifest ----------

info "核对 manifest"
# manifest.json 是「path / sha256 / size」的数组,一个文件一个对象;用 sed 拆成行,不依赖 jq。
awk '
  /"path"[[:space:]]*:/    { p=$0; sub(/.*"path"[[:space:]]*:[[:space:]]*"/, "", p); sub(/".*/, "", p) }
  /"sha256"[[:space:]]*:/  { s=$0; sub(/.*"sha256"[[:space:]]*:[[:space:]]*"/, "", s); sub(/".*/, "", s) }
  /"size"[[:space:]]*:/    { z=$0; sub(/.*"size"[[:space:]]*:[[:space:]]*/, "", z); sub(/[^0-9].*/, "", z);
                             if (p != "" && s != "") { print p, s, z; p=""; s=""; z="" } }
' "$SRC/manifest.json" > "$TMP/files.txt"
[ -s "$TMP/files.txt" ] || die "manifest.json 里没解析出文件清单"

# 大小逐个核(便宜),校验和批量核:路由器上 800 个文件逐个起进程太慢,能一次算完就一次算完
COUNT=0
: >"$TMP/check.txt"
while read -r path sum size; do
  f="$SRC/$path"
  [ -f "$f" ] || die "包里缺文件:$path"
  if [ -n "$size" ]; then
    actual=$(wc -c < "$f" | tr -d ' ')
    [ "$actual" = "$size" ] || die "$path 的大小是 $actual,清单写的是 $size"
  fi
  printf '%s  %s\n' "$sum" "$path" >>"$TMP/check.txt"
  COUNT=$((COUNT + 1))
done <"$TMP/files.txt"

if command -v sha256sum >/dev/null 2>&1; then
  # 不用 --quiet:busybox 的 sha256sum 不认这个长选项。失败时只把不是 OK 的那几行打出来
  if ! ( cd "$SRC" && sha256sum -c "$TMP/check.txt" >"$TMP/check.out" 2>&1 ); then
    grep -v ': OK$' "$TMP/check.out" >&2 || true
    die "文件校验和对不上(上面那条就是)"
  fi
else
  while read -r sum path; do
    got=$(sha256_of "$SRC/$path")
    [ "$got" = "$sum" ] || die "$path 的 sha256 是 $got,清单写的是 $sum"
  done <"$TMP/check.txt"
fi
info "核对通过:$COUNT 个文件"

# ---------- 4. 冲突检测 ----------

CONFLICTS="openclash passwall passwall2 homeproxy shadowsocksr nikki mihomo clash clash-meta v2raya v2ray xray"
FOUND=""
if [ "$REAL_ROOT" = 1 ]; then
  for name in $CONFLICTS; do
    if [ "$PLATFORM" = openwrt ]; then
      [ -x "/etc/init.d/$name" ] && FOUND="$FOUND $name"
    else
      if command -v systemctl >/dev/null 2>&1; then
        systemctl list-unit-files "$name.service" 2>/dev/null | grep -q "$name.service" && FOUND="$FOUND $name"
      fi
    fi
  done
fi
if [ -n "$FOUND" ]; then
  if [ "$FORCE" = 1 ]; then
    info "检测到其它代理服务:$FOUND(已按 --force 继续)"
  else
    die "检测到其它代理服务:$FOUND。它们会和本面板抢 DNS / 防火墙,请先停用;确实要继续请加 --force"
  fi
fi

# ---------- 5. 放文件 ----------

DEST="$ROOT/opt/open-box"
info "安装到 $DEST"

mkdir -p "$DEST/bin" "$DEST/etc" "$DEST/data" "$DEST/panel" "$DEST/geodata"
cp -f "$SRC/bin/"* "$DEST/bin/" || die "复制 bin 失败"
chmod 0755 "$DEST/bin/"* 2>/dev/null || true
[ -f "$DEST/bin/core-build.json" ] && chmod 0644 "$DEST/bin/core-build.json"

# 前端目录:先放到 .new,再把旧的挪成 .old,最后换位——中途失败也不会留下半个前端
if [ -d "$DEST/panel/dist" ]; then
  ( cd "$DEST/panel" && rm -rf dist.old && mv dist dist.old ) || die "挪走旧前端失败"
fi
cp -R "$SRC/panel/dist" "$DEST/panel/dist" || die "复制前端失败"
( cd "$DEST/panel" && rm -rf dist.old )

# 同步来的 openwrt / debian(内核服务脚本、LuCI、安装辅助)
for d in openwrt debian; do
  [ -d "$SRC/$d" ] || continue
  ( cd "$DEST" && rm -rf "$d" )
  cp -R "$SRC/$d" "$DEST/$d" || die "复制 $d 失败"
done

if [ -d "$SRC/geodata" ] && ls "$SRC/geodata"/*.srs >/dev/null 2>&1; then
  cp -f "$SRC/geodata/"*.srs "$DEST/geodata/" || die "复制规则库失败"
  info "已放随包规则库"
else
  info "这个包没带规则库,首次启动后按面板提示下载"
fi

# ---------- 6. 服务脚本 ----------

if [ "$PLATFORM" = openwrt ]; then
  [ -f "$SRC/service/openwrt/openbox-backend" ] || die "包里没有 procd 脚本"
  mkdir -p "$(p /etc/init.d)"
  cp -f "$SRC/service/openwrt/openbox-backend" "$(p /etc/init.d/openbox-backend)" || die "装 procd 脚本失败"
  chmod 0755 "$(p /etc/init.d/openbox-backend)" 2>/dev/null || true
else
  [ -f "$SRC/service/systemd/openbox-backend.service" ] || die "包里没有 systemd 单元"
  mkdir -p "$(p /etc/systemd/system)"
  cp -f "$SRC/service/systemd/openbox-backend.service" "$(p /etc/systemd/system/openbox-backend.service)" || die "装 systemd 单元失败"
  chmod 0644 "$(p /etc/systemd/system/openbox-backend.service)" 2>/dev/null || true
fi

# 面板端口:沿用已有的(升级不动它),没有就按 --port 或 2026 写一份
if [ ! -f "$DEST/data/panel-port" ]; then
  [ -n "$PORT" ] || PORT=2026
  printf '%s\n' "$PORT" > "$DEST/data/panel-port"
  info "面板端口:$PORT"
else
  info "面板端口沿用已有设置:$(cat "$DEST/data/panel-port")"
fi

# ---------- 7. 启用并只启动后端 ----------

if [ "$START" = 0 ]; then
  info "按 --no-start 不启动服务"
elif [ "$REAL_ROOT" = 0 ]; then
  info "假根目录:跳过服务管理器的启用与启动"
elif [ "$PLATFORM" = openwrt ]; then
  /etc/init.d/openbox-backend enable || die "启用 openbox-backend 失败"
  /etc/init.d/openbox-backend restart || die "启动 openbox-backend 失败"
  info "已启动后端(内核与 DNS 接管请在面板里点)"
else
  systemctl daemon-reload || true
  systemctl enable openbox-backend >/dev/null 2>&1 || die "启用 openbox-backend 失败"
  systemctl restart openbox-backend || die "启动 openbox-backend 失败"
  info "已启动后端(内核与 DNS 接管请在面板里点)"
fi

PORT_NOW=$(cat "$DEST/data/panel-port" 2>/dev/null || echo 2026)
info "完成。面板:http://<路由器地址>:$PORT_NOW/"
info "升级不会动数据与配置目录:$DEST/data、$DEST/etc"
