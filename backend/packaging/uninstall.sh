#!/bin/sh
# Open-Box 卸载脚本(自研后端)。设计见 backend/docs/M6.md 第五节。
#
# 用法: sh uninstall.sh [--purge] [--root <假根目录>] [--no-cleanup] [--no-stop]
#
# 顺序不能反:
#  1. 先让后端自己撤销系统改动(openbox-backend cleanup:撤销 DNS 接管 → 停内核 → 拆虚拟终端)。
#     这一步失败就停,不继续删文件——DNS 还指着一个即将被删掉的服务,整个局域网会断网。
#  2. 停掉并禁用两个服务(后端 + 内核),再删二进制、前端、规则库、服务脚本。
#  3. 默认保留数据目录与配置目录(重装可恢复);--purge 才一起删。
set -eu

PROG=uninstall.sh
die() { echo "[$PROG] 错误:$*" >&2; exit 1; }
info() { echo "[$PROG] $*" >&2; }

ROOT=/
PURGE=0
DO_CLEANUP=1
DO_STOP=1

while [ "$#" -gt 0 ]; do
  case "$1" in
    --purge) PURGE=1; shift ;;
    --root) [ "$#" -ge 2 ] || die "--root 后面要跟目录"; ROOT=$2; shift 2 ;;
    --no-cleanup) DO_CLEANUP=0; shift ;;
    --no-stop) DO_STOP=0; shift ;;
    -h|--help) sed -n '2,14p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "不认识的参数:$1" ;;
  esac
done

case "$ROOT" in
  */) ROOT=${ROOT%/} ;;
esac
[ -z "$ROOT" ] && ROOT=/
p() { printf '%s%s' "$ROOT" "$1"; }
REAL_ROOT=0
[ "$ROOT" = / ] && REAL_ROOT=1

if [ -f "$(p /etc/openwrt_release)" ]; then
  PLATFORM=openwrt
elif [ -f "$(p /etc/os-release)" ]; then
  PLATFORM=debian
else
  PLATFORM=unknown
fi

DEST="$ROOT/opt/open-box"

# ---------- 1. 先让后端自己收干净 ----------

if [ "$DO_CLEANUP" = 0 ]; then
  info "按 --no-cleanup 跳过撤销系统改动"
elif [ ! -f "$DEST/bin/openbox-backend" ]; then
  # 用 -f 而不是 -x:解包出来的权限位在有的文件系统 / 容器层上不生效,能跑不能跑交给它自己报错
  info "没有 $DEST/bin/openbox-backend,跳过撤销(可能已经卸过)"
elif [ "$REAL_ROOT" = 0 ]; then
  info "假根目录:跳过撤销系统改动"
else
  info "撤销 DNS 接管、停内核、拆虚拟终端"
  "$DEST/bin/openbox-backend" cleanup \
    -data-dir "$DEST/data" -config-dir "$DEST/etc" \
    || die "cleanup 失败,已停止(没有删任何文件)。请先排查:面板里能不能停内核、DNS 是否已恢复"
fi

# ---------- 2. 停并禁用服务 ----------

if [ "$DO_STOP" = 0 ] || [ "$REAL_ROOT" = 0 ]; then
  info "跳过停用服务"
elif [ "$PLATFORM" = openwrt ]; then
  for svc in openbox-backend openbox; do
    if [ -x "/etc/init.d/$svc" ]; then
      /etc/init.d/$svc stop 2>/dev/null || info "停 $svc 失败(继续)"
      /etc/init.d/$svc disable 2>/dev/null || true
    fi
  done
else
  for svc in openbox-backend openbox; do
    if command -v systemctl >/dev/null 2>&1; then
      systemctl disable --now "$svc" >/dev/null 2>&1 || true
    fi
  done
fi

# ---------- 3. 删文件 ----------

info "删二进制、前端、规则库、同步来的脚本"
# 用相对路径(先 cd 到安装根):本机沙箱的删除保护会拦带盘符的路径,真机上等价
if [ -d "$DEST" ]; then
  ( cd "$DEST" && rm -rf bin panel geodata openwrt debian )
fi

if [ "$PLATFORM" = openwrt ]; then
  rm -f "$(p /etc/init.d/openbox-backend)"
elif [ "$PLATFORM" = debian ]; then
  rm -f "$(p /etc/systemd/system/openbox-backend.service)"
  if [ "$REAL_ROOT" = 1 ] && command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
  fi
fi

if [ "$PURGE" = 1 ]; then
  info "按 --purge 删数据与配置"
  if [ -d "$DEST" ]; then
    ( cd "$DEST" && rm -rf etc data )
  fi
  rmdir "$DEST" 2>/dev/null || true
else
  info "保留数据与配置:$DEST/data、$DEST/etc(要一起删请加 --purge)"
fi

info "卸载完成"
