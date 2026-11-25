#!/bin/sh
# 从 versions.env 钉住的「补丁源码」编两个内核(双内核第 7 步 / M6 第六节 c)。
#
# 用法: sh backend/packaging/build-kernels.sh <amd64|arm64> <outdir> [sing-box|mihomo|all]
# 产出: <outdir>/sing-box、<outdir>/mihomo,都是 linux 静态二进制(CGO_ENABLED=0)。
#
# 为什么自己编:发布包里的内核必须是打过 open-box 补丁的那份(sing-box 的 node_score_weights / bandwidth /
# openbox-* 字段、Mihomo 的 openbox-* 补丁键),官方二进制没有这些,配置会起不来或功能不全。
# 上游把补丁源码作为 release 资产公开,所以这里下载源码自己编,不碰闭源产物。
#
# 缓存:源码包与解包结果放 ${OPENBOX_KERNEL_CACHE:-$TMPDIR/openbox-kernels/<两个源码包的 sha256 前 12 位>},
# 同一台机器上重复构建不会重新下载。校验和每次都核,缓存命中也要核。
# 缓存键用 sha256 而不是版本串:v0.1.482 → v0.1.484 时 sing-box 的补丁源码包**同名但内容变了**
# (版本串还是 1.14.4-openbox-60),按版本串做键会一直用旧的那份,然后在校验和上失败。
set -eu

die() { echo "[build-kernels] 错误:$*" >&2; exit 1; }
# 日志走 stderr:unpack 用 $(...) 取「解包后的路径」,日志混进 stdout 会让 SRC 变成两行
info() { echo "[build-kernels] $*" >&2; }

[ "$#" -ge 2 ] || die "用法: $0 <amd64|arm64> <outdir> [sing-box|mihomo|all]"

case "$1" in
  amd64|x64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "不支持的架构:$1(只认 amd64 / arm64)" ;;
esac
WHICH="${3:-all}"
case "$WHICH" in
  sing-box|mihomo|all) ;;
  *) die "第三个参数只认 sing-box / mihomo / all,得到:$WHICH" ;;
esac

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=/dev/null
. "$HERE/versions.env"

mkdir -p "$2"
OUT=$(CDPATH= cd -- "$2" && pwd)

TMPBASE="${TMPDIR:-/tmp}"
_sb_key=$(printf '%s' "$SINGBOX_SOURCE_SHA256" | cut -c1-12)
_mh_key=$(printf '%s' "$MIHOMO_SOURCE_SHA256" | cut -c1-12)
CACHE="${OPENBOX_KERNEL_CACHE:-$TMPBASE/openbox-kernels/$_sb_key-$_mh_key}"
mkdir -p "$CACHE"
CACHE=$(CDPATH= cd -- "$CACHE" && pwd)

# sha256_of <文件>:优先 sha256sum(linux / busybox),其次 shasum -a 256(macOS),再其次 openssl
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

# fetch <资产名> <sha256>:下载到缓存并校验,已存在就只校验
fetch() {
  _asset=$1; _want=$2
  _file="$CACHE/$_asset"
  if [ ! -f "$_file" ]; then
    _url="$KERNEL_SOURCE_BASE/$_asset"
    info "下载 $_asset"
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL --retry 3 --connect-timeout 20 -o "$_file.part" "$_url" || die "下载失败:$_url"
    elif command -v wget >/dev/null 2>&1; then
      wget -q -O "$_file.part" "$_url" || die "下载失败:$_url"
    else
      die "找不到 curl / wget,没法下载 $_url"
    fi
    mv "$_file.part" "$_file"
  fi
  _got=$(sha256_of "$_file")
  [ "$_got" = "$_want" ] || {
    rm -f "$_file"
    die "$_asset 的 sha256 是 $_got,versions.env 写的是 $_want(已删掉这个文件,请核对来源)"
  }
}

# unpack <资产名> <顶层目录名>:解到缓存里的 src/,已经解过就跳过
unpack() {
  _asset=$1; _top=$2
  _dir="$CACHE/src/$_top"
  if [ ! -d "$_dir" ]; then
    info "解包 $_asset"
    mkdir -p "$CACHE/src"
    rm -rf "$CACHE/src/.unpack"
    mkdir -p "$CACHE/src/.unpack"
    case "$_asset" in
      *.tar.gz|*.tgz) tar xzf "$CACHE/$_asset" -C "$CACHE/src/.unpack" || die "解包 $_asset 失败" ;;
      *.tar.xz)       tar xJf "$CACHE/$_asset" -C "$CACHE/src/.unpack" || die "解包 $_asset 失败" ;;
      *)              die "不认识的压缩格式:$_asset" ;;
    esac
    [ -d "$CACHE/src/.unpack/$_top" ] || die "$_asset 里没有顶层目录 $_top(源码包结构变了?)"
    mv "$CACHE/src/.unpack/$_top" "$_dir"
    rmdir "$CACHE/src/.unpack" 2>/dev/null || true
  fi
  printf '%s\n' "$_dir"
}

build_singbox() {
  fetch "$SINGBOX_SOURCE_ASSET" "$SINGBOX_SOURCE_SHA256"
  SRC=$(unpack "$SINGBOX_SOURCE_ASSET" "$SINGBOX_SOURCE_DIR")
  [ -f "$SRC/go.mod" ] || die "$SRC 里没有 go.mod"
  # 构建标签从源码包自己带的清单读,不写死:上游改了标签清单我们不用跟着改
  TAGS=$(tr -d '\r\n' < "$SRC/release/DEFAULT_BUILD_TAGS_OTHERS")
  [ -n "$TAGS" ] || die "读不到 $SRC/release/DEFAULT_BUILD_TAGS_OTHERS"
  info "编 sing-box $SINGBOX_BUILD_VERSION($ARCH)"
  ( cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
      go build -trimpath -tags "$TAGS" \
        -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=$SINGBOX_BUILD_VERSION' -s -w -buildid=" \
        -o "$OUT/sing-box" ./cmd/sing-box ) || die "sing-box 编译失败"
  [ -s "$OUT/sing-box" ] || die "没有产出 $OUT/sing-box"
  chmod 0755 "$OUT/sing-box"
}

build_mihomo() {
  fetch "$MIHOMO_SOURCE_ASSET" "$MIHOMO_SOURCE_SHA256"
  SRC=$(unpack "$MIHOMO_SOURCE_ASSET" "$MIHOMO_SOURCE_DIR")
  [ -f "$SRC/go.mod" ] || die "$SRC 里没有 go.mod"
  info "编 Mihomo $MIHOMO_BUILD_VERSION($ARCH)"
  # 与 mihomo 自己的 Makefile 一致:-tags with_gvisor,版本与构建时间走 constant 包的 -X
  ( cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
      go build -tags with_gvisor -trimpath \
        -ldflags "-X 'github.com/metacubex/mihomo/constant.Version=$MIHOMO_BUILD_VERSION' -X 'github.com/metacubex/mihomo/constant.BuildTime=$BUILD_TIME' -w -s" \
        -o "$OUT/mihomo" . ) || die "Mihomo 编译失败"
  [ -s "$OUT/mihomo" ] || die "没有产出 $OUT/mihomo"
  chmod 0755 "$OUT/mihomo"
}

BUILD_TIME="${SOURCE_DATE_EPOCH:+$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

case "$WHICH" in
  sing-box) build_singbox ;;
  mihomo) build_mihomo ;;
  all) build_singbox; build_mihomo ;;
esac

info "完成:$OUT"
