#!/bin/sh
# openbox-backend-run 的假根目录测试:不需要 root,不碰真系统。
#
# 做法:造一个临时安装根,里面放一个「把参数原样打出来」的假后端,然后跑启动脚本,
# 断言它传下去的每个参数都对(端口尤其重要:用户从上游换过来时书签不能失效)。
#
# 临时目录用当前目录下的相对路径:本机沙箱的删除保护会拦带盘符的递归删除,
# 相对路径才删得掉。脚本自己 cd 到所在目录,所以从哪儿调用都一样。
set -eu

here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

tmp=.tmp-test-runner
rm -rf "$tmp"
mkdir -p "$tmp"
trap 'rm -rf "$tmp"' EXIT

root="$tmp/root"
mkdir -p "$root/bin" "$root/data"
cat >"$root/bin/openbox-backend" <<'EOF'
#!/bin/sh
printf '%s\n' "$@"
EOF
chmod +x "$root/bin/openbox-backend"

fail() {
	echo "失败:$*" >&2
	exit 1
}

# 参数里必须有这个键,而且下一行必须是这个值
expect_flag() {
	out=$1 key=$2 val=$3
	echo "$out" | grep -qx -- "$key" || fail "没有 $key(实际:$out)"
	got=$(echo "$out" | awk -v k="$key" '$0==k{getline; print; exit}')
	[ "$got" = "$val" ] || fail "$key 应为 $val,实际 $got"
}

# 1. 没有 panel-port 文件:用 2026(与上游面板的默认端口一致)
out=$(OPENBOX_ROOT="$root" sh ./openbox-backend-run)
expect_flag "$out" -listen "0.0.0.0:2026"
expect_flag "$out" -config-dir "$root/etc"
expect_flag "$out" -data-dir "$root/data"
expect_flag "$out" -web-dir "$root/panel/dist"
expect_flag "$out" -geo-dir "$root/geodata"
expect_flag "$out" -sing-box "$root/bin/sing-box"
expect_flag "$out" -mihomo "$root/bin/mihomo"
expect_flag "$out" -core "$root/bin/openbox-core"
expect_flag "$out" -core-service "openbox"
expect_flag "$out" -panel-service "openbox-backend"

# 2. panel-port 里是装的时候选的端口:照它来
printf '8080\n' >"$root/data/panel-port"
out=$(OPENBOX_ROOT="$root" sh ./openbox-backend-run)
expect_flag "$out" -listen "0.0.0.0:8080"

# 3. 文件里混了空格 / 回车:只留数字(与上游 init 脚本的 tr -dc '0-9' 同口径)
printf ' 9090 \r\n' >"$root/data/panel-port"
out=$(OPENBOX_ROOT="$root" sh ./openbox-backend-run)
expect_flag "$out" -listen "0.0.0.0:9090"

# 4. OPENBOX_LISTEN 直接覆盖完整监听地址
out=$(OPENBOX_ROOT="$root" OPENBOX_LISTEN="127.0.0.1:1234" sh ./openbox-backend-run)
expect_flag "$out" -listen "127.0.0.1:1234"

# 5. 没有后端二进制:退出码非 0,并说清楚缺什么
rm -f "$root/bin/openbox-backend"
if OPENBOX_ROOT="$root" sh ./openbox-backend-run >"$tmp/out" 2>"$tmp/err"; then
	fail "缺二进制时应当失败"
fi
grep -q "找不到" "$tmp/err" || fail "缺二进制时应当提示路径,实际:$(cat "$tmp/err")"

# 6. 数据目录会被建出来(procd / systemd 第一次起时它可能还不存在)
rm -rf "$root/data"
cat >"$root/bin/openbox-backend" <<'EOF'
#!/bin/sh
printf '%s\n' "$@"
EOF
chmod +x "$root/bin/openbox-backend"
out=$(OPENBOX_ROOT="$root" sh ./openbox-backend-run)
[ -d "$root/data" ] || fail "应当建出数据目录"

echo "openbox-backend-run:6 组断言全过"
