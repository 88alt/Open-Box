#!/bin/sh
# 编 openbox-core(M6-1)。
#
# 用法: sh backend/packaging/build-core.sh <amd64|arm64> <outdir>
# 产出: <outdir>/openbox-core、<outdir>/core-build.json(由 core/scripts/build.sh 写)
#
# 为什么不直接调 core/scripts/build.sh 就算完:那个脚本的第三个参数默认指向
# panel/server/index.mjs(上游闭源产物),在它存在时会从里面抠 buildID。后端不校验 buildID(DESIGN D5),
# 这里显式给一个**不存在**的路径,强制走「按 core 源码内容算 sha256」那条确定性路径,
# 同一份源码永远得到同一个 buildID,也不依赖任何上游产物。
set -eu

die() { echo "[build-core] 错误:$*" >&2; exit 1; }

[ "$#" -ge 2 ] || die "用法: $0 <amd64|arm64> <outdir>"

case "$1" in
  amd64|x64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "不支持的架构:$1(只认 amd64 / arm64)" ;;
esac

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BACKEND=$(CDPATH= cd -- "$HERE/.." && pwd)
REPO=$(CDPATH= cd -- "$BACKEND/.." && pwd)
CORE="$REPO/core"
[ -f "$CORE/scripts/build.sh" ] || die "找不到 $CORE/scripts/build.sh"

mkdir -p "$2"
OUT=$(CDPATH= cd -- "$2" && pwd)

# 第三个参数是 index.mjs 的位置。给一个不会存在的路径 = 不读上游产物。
NO_UPSTREAM_INPUT="$OUT/.no-upstream-input"

echo "[build-core] arch=$ARCH out=$OUT"
sh "$CORE/scripts/build.sh" "$ARCH" "$OUT" "$NO_UPSTREAM_INPUT"

# 用 -s(非空普通文件)而不是 -x:在 Windows 上交叉编译出来的文件没有执行位,go build 在 linux 上本来就是 0755。
# 这里统一 chmod,让两种环境产出的包权限一致。
[ -s "$OUT/openbox-core" ] || die "没有产出 $OUT/openbox-core"
[ -f "$OUT/core-build.json" ] || die "没有产出 $OUT/core-build.json"
chmod 0755 "$OUT/openbox-core"
echo "[build-core] 完成:$OUT/openbox-core"
