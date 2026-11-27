#!/bin/sh
# DNS 接管真机验证的「备份 + 自动回滚」脚本(在路由器上跑,不在本机跑)。
#
# 用法(在路由器上):
#   sh dns-backup-rollback.sh backup       # 只备份,记下回滚点
#   sh dns-backup-rollback.sh arm 300      # 备份并挂上「300 秒后自动回滚」的定时回滚
#   sh dns-backup-rollback.sh cancel       # 验证通过,取消自动回滚
#   sh dns-backup-rollback.sh restore      # 手动立刻回滚
#
# 为什么要它:维护者要求 DNS 接管每动一项之前先备份、并挂一个定时自动回滚,
# 万一改完局域网没 DNS,到点自己恢复,不用等人救。
#
# 回滚做三件事:① 还原本脚本备份的 dnsmasq 配置目录内容;
# ② 重启 dnsmasq;③ 打印回滚后的状态供核对。

set -u
BK_ROOT=/root/obx-dns-backup
CONF_DIR_DEFAULT=/tmp/dnsmasq.cfg01411c.d
STATE="$BK_ROOT/current"

log() { echo "[$(date '+%F %T')] $*"; }

# conf-dir 从实际生效的 dnsmasq 主配置里读,读不到用默认值
conf_dir() {
    d=$(grep -h '^conf-dir=' /var/etc/dnsmasq.conf.* 2>/dev/null | head -1 | cut -d= -f2 | cut -d, -f1)
    [ -n "$d" ] && echo "$d" || echo "$CONF_DIR_DEFAULT"
}

do_backup() {
    ts=$(date '+%Y%m%d-%H%M%S')
    dest="$BK_ROOT/$ts"
    mkdir -p "$dest" || exit 1
    cd=$(conf_dir)

    # 1) dnsmasq 配置目录(整个拷,含 openbox-dns.conf、open-box.conf 这类接管文件)
    if [ -d "$cd" ]; then
        cp -a "$cd" "$dest/dnsmasq.d" 2>/dev/null
        echo "$cd" > "$dest/conf-dir.txt"
    fi
    # 2) 主配置与静态配置
    cp -a /var/etc/dnsmasq.conf.* "$dest/" 2>/dev/null
    cp -a /etc/config/dhcp "$dest/dhcp.uci" 2>/dev/null
    cp -a /etc/dnsmasq.conf "$dest/dnsmasq.conf" 2>/dev/null
    # 3) 记录当时状态
    {
        echo "conf_dir=$cd"
        echo "dnsmasq_pids=$(pgrep dnsmasq | tr '\n' ' ')"
        echo "router_kernel=$(cat /opt/open-box/etc/router-kernel 2>/dev/null)"
        echo "takeover_files=$(ls "$cd" 2>/dev/null | tr '\n' ' ')"
    } > "$dest/state.txt"

    ln -sfn "$dest" "$STATE"
    log "备份完成: $dest"
    log "回滚点已指向: $(readlink -f "$STATE")"
    echo "$dest"
}

do_restore() {
    [ -d "$STATE" ] || { log "没有回滚点,拒绝执行"; exit 1; }
    dest=$(readlink -f "$STATE")
    log "从 $dest 回滚"

    cd=$(conf_dir)
    saved_cd=$(cat "$dest/conf-dir.txt" 2>/dev/null)
    [ -n "$saved_cd" ] && cd="$saved_cd"

    # 1) 还原 dnsmasq 配置目录:先清掉现在目录里的文件(否则新的接管文件会残留),再把备份放回
    if [ -d "$dest/dnsmasq.d" ] && [ -d "$cd" ]; then
        rm -f "$cd"/* 2>/dev/null
        cp -a "$dest/dnsmasq.d/." "$cd/" 2>/dev/null
        log "已还原 $cd"
    fi
    # 2) 还原 /etc/config/dhcp
    [ -f "$dest/dhcp.uci" ] && cp -a "$dest/dhcp.uci" /etc/config/dhcp && log "已还原 /etc/config/dhcp"
    # 3) 还原 /etc/dnsmasq.conf
    [ -f "$dest/dnsmasq.conf" ] && cp -a "$dest/dnsmasq.conf" /etc/dnsmasq.conf && log "已还原 /etc/dnsmasq.conf"
    # 4) 重启 dnsmasq 并确认
    /etc/init.d/dnsmasq restart >/dev/null 2>&1
    sleep 3
    if pgrep dnsmasq >/dev/null; then
        log "dnsmasq 已重启,pid=$(pgrep dnsmasq | tr '\n' ' ')"
    else
        log "警告:dnsmasq 没起来,请人工检查"
    fi
    # 5) 自检:本机能不能解析
    if nslookup www.baidu.com 127.0.0.1 >/dev/null 2>&1; then
        log "自检通过:本机 dnsmasq 能解析"
    else
        log "警告:本机 dnsmasq 解析失败,请人工检查 $cd"
    fi
}

do_arm() {
    secs=${1:-300}
    dest=$(do_backup) || exit 1
    # 用 nohup + sleep 挂一个脱离会话的定时任务;它只在本机跑,重启后失效(那时 dnsmasq 也是新起的)
    nohup sh -c "sleep $secs; sh $(cd "$(dirname "$0")" && pwd)/$(basename "$0") restore; echo '=== 自动回滚已执行 ==='" \
        > "$BK_ROOT/auto-rollback.log" 2>&1 &
    log "已挂自动回滚:${secs} 秒后执行(日志 $BK_ROOT/auto-rollback.log)"
    log "验证通过后请执行:sh $0 cancel"
}

do_cancel() {
    # 只杀我们这个脚本挂的那个 sleep/sh,不碰别的
    for p in $(pgrep -f "sleep .*; sh .*dns-backup-rollback.sh restore" 2>/dev/null); do
        kill "$p" 2>/dev/null && log "已取消定时回滚(pid $p)"
    done
    pkill -f "dns-backup-rollback.sh restore" 2>/dev/null
    log "取消完成(回滚点仍保留在 $STATE,需要时手动 restore)"
}

case "${1:-}" in
    backup)  do_backup ;;
    arm)     do_arm "${2:-300}" ;;
    cancel)  do_cancel ;;
    restore) do_restore ;;
    *) echo "用法: $0 {backup|arm [秒数]|cancel|restore}"; exit 2 ;;
esac
