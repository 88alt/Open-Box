#!/bin/sh
# 摆好一个架构的安装目录并打成发布包(M6 第三节 / 第六节 e)。
#
# 用法: sh backend/packaging/build-package.sh <amd64|arm64> <outdir> [选项]
#   --version <版本号>    发布版本号,默认 v0.1.<上游号>-main.local(正式发布必须显式给)
#   --panel-dist <目录>   面板构建产物,默认 <仓库>/panel/dist(要事先 pnpm build)
#   --geodata <目录>      随包规则库(里面是 <分类名>.srs);不给就不随包
#   --skip-kernels        不编内核(本地冒烟用:包里没有 bin/sing-box / bin/mihomo)
#   --keep-staging        保留暂存目录,便于检查摆得对不对
#
# 产出:<outdir>/openbox-<版本>-linux-<架构>.tar.gz 与 SHA256SUMS。
#
# 目录布局见 backend/docs/M6.md 第四节(跟 /opt/open-box 走)。包里的 openwrt/ 与 debian/
# 是同步来的上游脚本(内核服务、LuCI、安装辅助),install.sh 从包里取它们;我们自己的后端服务脚本
# 在 service/ 下,install.sh 按平台挑一个装。
set -eu

die() { echo "[build-package] 错误:$*" >&2; exit 1; }
info() { echo "[build-package] $*" >&2; }

[ "$#" -ge 2 ] || die "用法: $0 <amd64|arm64> <outdir> [--version vX] [--panel-dist DIR] [--geodata DIR] [--skip-kernels] [--keep-staging]"

case "$1" in
  amd64|x64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "不支持的架构:$1(只认 amd64 / arm64)" ;;
esac
OUT_ARG=$2
shift 2

VERSION=""
PANEL_DIST=""
GEODATA_DIR=""
SKIP_KERNELS=0
KEEP_STAGING=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || die "--version 后面要跟版本号"; VERSION=$2; shift 2 ;;
    --panel-dist) [ "$#" -ge 2 ] || die "--panel-dist 后面要跟目录"; PANEL_DIST=$2; shift 2 ;;
    --geodata) [ "$#" -ge 2 ] || die "--geodata 后面要跟目录"; GEODATA_DIR=$2; shift 2 ;;
    --skip-kernels) SKIP_KERNELS=1; shift ;;
    --keep-staging) KEEP_STAGING=1; shift ;;
    *) die "不认识的参数:$1" ;;
  esac
done

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO=$(CDPATH= cd -- "$HERE/../.." && pwd)
# shellcheck source=/dev/null
. "$HERE/versions.env"

VERSION="${VERSION:-v0.1.$OPENBOX_UPSTREAM-main.local}"
PANEL_DIST="${PANEL_DIST:-$REPO/panel/dist}"

command -v go >/dev/null 2>&1 || die "找不到 go"
[ -d "$PANEL_DIST" ] || die "找不到面板构建产物 $PANEL_DIST(先在 panel/ 里 pnpm build)"
[ -f "$PANEL_DIST/index.html" ] || die "$PANEL_DIST 里没有 index.html,不像是 panel/dist"

mkdir -p "$OUT_ARG"
OUT=$(CDPATH= cd -- "$OUT_ARG" && pwd)
TOP="openbox-$VERSION-linux-$ARCH"
STAGE="$OUT/.staging-$ARCH/$TOP"
# 上一次没清干净的暂存目录:直接删掉重建,免得把旧文件打进包
( cd "$OUT" && rm -rf ".staging-$ARCH" )
mkdir -p "$STAGE/bin"

BUILT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)

info "编后端($ARCH,$VERSION)"
( cd "$REPO/backend" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.builtAt=$BUILT_AT" \
      -o "$STAGE/bin/openbox-backend" ./cmd/openbox-backend ) || die "后端编译失败"

info "编 openbox-core"
sh "$HERE/build-core.sh" "$ARCH" "$STAGE/bin" >&2 || die "openbox-core 编译失败"

if [ "$SKIP_KERNELS" = 1 ]; then
  info "跳过内核(--skip-kernels)"
else
  info "编内核(sing-box + Mihomo)"
  sh "$HERE/build-kernels.sh" "$ARCH" "$STAGE/bin" all >&2 || die "内核编译失败"
fi

info "放面板"
mkdir -p "$STAGE/panel"
cp -R "$PANEL_DIST" "$STAGE/panel/dist" || die "复制面板失败"

info "放同步来的 openwrt / debian"
for d in openwrt debian; do
  [ -d "$REPO/$d" ] || die "仓库里没有 $d 目录"
  cp -R "$REPO/$d" "$STAGE/$d" || die "复制 $d 失败"
done

info "放后端服务脚本"
mkdir -p "$STAGE/service/openwrt" "$STAGE/service/systemd"
cp "$HERE/service/openbox-backend-run" "$STAGE/bin/openbox-backend-run" || die "复制启动脚本失败"
cp "$HERE/service/openwrt/openbox-backend" "$STAGE/service/openwrt/openbox-backend" || die "复制 procd 脚本失败"
cp "$HERE/service/systemd/openbox-backend.service" "$STAGE/service/systemd/openbox-backend.service" || die "复制 systemd 单元失败"

if [ -n "$GEODATA_DIR" ]; then
  [ -d "$GEODATA_DIR" ] || die "找不到规则库目录 $GEODATA_DIR"
  info "放规则库"
  mkdir -p "$STAGE/geodata"
  cp "$GEODATA_DIR"/*.srs "$STAGE/geodata/" 2>/dev/null || die "规则库目录里没有 .srs"
else
  info "不随包规则库(M6 第十二节第 4 条:来源与许可待维护者核对)"
fi

# 清掉 macOS 的垃圾文件:同步来的目录里偶尔带 .DS_Store,不能进包。
# 用相对路径(cd 到暂存目录里再删):本机沙箱的删除保护会拦带盘符的路径。
( cd "$STAGE" && find . -name '.DS_Store' -delete 2>/dev/null || true )

# 权限表:openwrt/ debian/ 是同步来的,权限以 git 记的为准(100755 → 0755,其余 0644);
# 我们自己的文件按「该执行的 0755、其余 0644」。不读磁盘权限:Windows 上 chmod 是空操作。
MODES="$OUT/.staging-$ARCH/modes.txt"
{
  echo "# 由 build-package.sh 生成:git 里记的权限 + 我们自己的文件"
  ( cd "$REPO" && git ls-files -s openwrt debian ) | while read -r mode _ _ path; do
    case "$mode" in
      100755) echo "0755 $path" ;;
      *) echo "0644 $path" ;;
    esac
  done
  for f in $( cd "$STAGE" && find bin -type f ); do
    case "$f" in
      bin/core-build.json) echo "0644 $f" ;;
      *) echo "0755 $f" ;;
    esac
  done
  echo "0755 service/openwrt/openbox-backend"
  echo "0644 service/systemd/openbox-backend.service"
} > "$MODES"

info "写清单并打包"
if [ -n "$GEODATA_DIR" ]; then
  ( cd "$REPO/backend" && go run ./packaging/pack \
      -root "$STAGE" -version "$VERSION" -arch "$ARCH" \
      -upstream "$OPENBOX_UPSTREAM" -out "$OUT" -built-at "$BUILT_AT" -modes "$MODES" \
      -singbox-version "$SINGBOX_BUILD_VERSION" \
      -geodata-version "$GEODATA_REF" -geodata-date "$(date -u +%Y-%m-%d)" ) || die "打包失败"
else
  ( cd "$REPO/backend" && go run ./packaging/pack \
      -root "$STAGE" -version "$VERSION" -arch "$ARCH" \
      -upstream "$OPENBOX_UPSTREAM" -out "$OUT" -built-at "$BUILT_AT" -modes "$MODES" \
      -singbox-version "$SINGBOX_BUILD_VERSION" ) || die "打包失败"
fi

if [ "$KEEP_STAGING" = 1 ]; then
  info "暂存目录保留在 $STAGE"
else
  ( cd "$OUT" && rm -rf ".staging-$ARCH" )
fi

info "完成:$OUT/openbox-$VERSION-linux-$ARCH.tar.gz"
ls -l "$OUT"/*.tar.gz "$OUT"/SHA256SUMS 2>/dev/null >&2 || true
