#!/bin/sh
# 发布打包入口(POSIX sh):sh scripts/build-release.sh <x64|arm64> <outdir>
#
# 上游自 v0.1.463 起是纯 Go 面板(无 Node、无明文后端),这里不再自己打包面板,
# 而是把 .upstream-synced 记录的上游同版本 release 重新封装(换仓库地址、重算 app 组件的哈希)。
# 做了什么、为什么,见 scripts/repack-upstream.py 开头的说明。
#
# 产出 <outdir>/:
#   open-box[-<版本>]-linux-<arch>.tar.gz(+ .sha256)   完整安装包(带版本号与不带版本号两份,内容相同)
#   open-box-<版本>-linux-<arch>-{app,kernel,kernel-mihomo,geo}.tar.gz(+ .sha256)   分组件包
#   open-box[-<版本>]-linux-<arch>-components.json(+ .sha256)   schema:2 组件清单
set -eu
[ "$#" -eq 2 ] || { echo "usage: $0 <x64|arm64> <outdir>" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: 需要 python3" >&2; exit 1; }
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$SCRIPT_DIR/repack-upstream.py" build "$1" "$2"
