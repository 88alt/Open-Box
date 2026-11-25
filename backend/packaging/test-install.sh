#!/bin/sh
# install.sh / uninstall.sh 的假根目录测试(M6 第五节末:不需要真机)。
#
# 做法:用打包工具造一个很小的假安装包(里面只有几个占位文件,不编译任何东西),
# 然后在临时「假根目录」里装一遍、再装一遍(幂等)、再卸两遍(保留 / --purge),
# 全程不碰真系统、不调服务管理器。
set -eu

here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

tmp=.tmp-install-test
rm -rf "$tmp"
mkdir -p "$tmp"
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "失败:$*" >&2
	exit 1
}

# ---------- 造一个假安装包 ----------

V=v9.9.9-test
TOP="openbox-$V-linux-amd64"
STAGE="$tmp/stage"
mkdir -p "$STAGE/bin" "$STAGE/panel/dist" "$STAGE/openwrt/initd" "$STAGE/debian/systemd" \
	"$STAGE/service/openwrt" "$STAGE/service/systemd" "$STAGE/geodata"

# 假后端:cleanup 时被 uninstall.sh 调用,记一笔并成功退出
cat >"$STAGE/bin/openbox-backend" <<'EOF'
#!/bin/sh
echo "$@" >> "$(dirname "$0")/../data/cleanup-called"
exit 0
EOF
printf '#!/bin/sh\nexit 0\n' >"$STAGE/bin/openbox-core"
printf '{"buildID":"deadbeef"}\n' >"$STAGE/bin/core-build.json"
printf '<html>panel</html>\n' >"$STAGE/panel/dist/index.html"
printf '#!/bin/sh\n' >"$STAGE/openwrt/initd/openbox"
printf '[Unit]\nDescription=fake\n' >"$STAGE/debian/systemd/openbox.service"
printf '#!/bin/sh\n' >"$STAGE/service/openwrt/openbox-backend"
printf '[Unit]\nDescription=backend\n' >"$STAGE/service/systemd/openbox-backend.service"
printf 'SRS\n' >"$STAGE/geodata/geosite-cn.srs"

cat >"$tmp/modes.txt" <<'EOF'
0755 bin/openbox-backend
0755 bin/openbox-core
0644 bin/core-build.json
0644 panel/dist/index.html
0755 openwrt/initd/openbox
0644 debian/systemd/openbox.service
0755 service/openwrt/openbox-backend
0644 service/systemd/openbox-backend.service
0644 geodata/geosite-cn.srs
EOF

DIST="$tmp/dist"
mkdir -p "$DIST"
( cd ../../backend && go run ./packaging/pack -root "$here/$STAGE" -version "$V" -arch amd64 \
	-upstream 484 -out "$here/$DIST" -built-at 2026-11-25T00:00:00Z -modes "$here/$tmp/modes.txt" ) >/dev/null \
	|| fail "造包失败"
TARBALL="$DIST/openbox-$V-linux-amd64.tar.gz"
[ -f "$TARBALL" ] || fail "没有产出 $TARBALL"
cp "$TARBALL" "$TARBALL.sha256.tmp"
cut -d' ' -f1 "$DIST/SHA256SUMS" >"$TARBALL.sha256"
rm -f "$TARBALL.sha256.tmp"

# ---------- 假根目录 ----------

ROOT="$tmp/root"
mkdir -p "$ROOT/etc"
: >"$ROOT/etc/openwrt_release" # 让 install.sh 认成 OpenWrt
DEST="$ROOT/opt/open-box"

# ---------- 1. 装 ----------

sh ./install.sh --root "$ROOT" --file "$TARBALL" --no-start >"$tmp/install.log" 2>&1 \
	|| { cat "$tmp/install.log" >&2; fail "第一次安装失败"; }

[ -x "$DEST/bin/openbox-backend" ] || fail "后端没装上"
[ -f "$DEST/panel/dist/index.html" ] || fail "前端没装上"
[ -d "$DEST/openwrt" ] || fail "openwrt 目录没装上"
[ -f "$ROOT/etc/init.d/openbox-backend" ] || fail "procd 脚本没装上(应装在 /etc/init.d)"
[ -f "$DEST/geodata/geosite-cn.srs" ] || fail "规则库没装上"
[ -f "$DEST/data/panel-port" ] || fail "没写 panel-port"
[ "$(cat "$DEST/data/panel-port")" = 2026 ] || fail "panel-port 默认值应为 2026"
[ -d "$DEST/etc" ] || fail "没建配置目录"
# 权限:bin 可执行、core-build.json 不可执行
[ -x "$DEST/bin/openbox-backend" ] || fail "bin 里的文件应当可执行"

# ---------- 2. 再装一遍:幂等,且不动数据 ----------

echo keep >"$DEST/data/marker"
printf '8080\n' >"$DEST/data/panel-port"
sh ./install.sh --root "$ROOT" --file "$TARBALL" --no-start >"$tmp/install2.log" 2>&1 \
	|| { cat "$tmp/install2.log" >&2; fail "第二次安装失败"; }
[ "$(cat "$DEST/data/marker")" = keep ] || fail "重装动了数据目录"
[ "$(cat "$DEST/data/panel-port")" = 8080 ] || fail "重装把端口改回去了(应沿用已有设置)"

# ---------- 3. 校验和不符要拦住 ----------

BAD="$tmp/bad"
mkdir -p "$BAD"
cp "$TARBALL" "$BAD/openbox-$V-linux-amd64.tar.gz"
printf '%064d\n' 0 >"$BAD/openbox-$V-linux-amd64.tar.gz.sha256"
if sh ./install.sh --root "$ROOT" --file "$BAD/openbox-$V-linux-amd64.tar.gz" --no-start >"$tmp/bad.log" 2>&1; then
	fail "校验和不符时应当失败"
fi
grep -q "校验和不符" "$tmp/bad.log" || fail "校验和不符时应报「校验和不符」,实际:$(cat "$tmp/bad.log")"

# ---------- 4. 卸(保留数据) ----------

sh ./uninstall.sh --root "$ROOT" >"$tmp/uninstall.log" 2>&1 \
	|| { cat "$tmp/uninstall.log" >&2; fail "卸载失败"; }
[ ! -e "$DEST/bin" ] || fail "bin 没删掉"
[ ! -e "$DEST/panel" ] || fail "panel 没删掉"
[ ! -e "$ROOT/etc/init.d/openbox-backend" ] || fail "服务脚本没删掉"
[ -f "$DEST/data/marker" ] || fail "默认应保留数据目录"
[ -d "$DEST/etc" ] || fail "默认应保留配置目录"

# ---------- 5. 卸 --purge:数据和配置一起删 ----------

sh ./uninstall.sh --root "$ROOT" --purge >"$tmp/uninstall2.log" 2>&1 \
	|| { cat "$tmp/uninstall2.log" >&2; fail "--purge 卸载失败"; }
[ ! -e "$DEST/data" ] || fail "--purge 应当删掉数据目录"
[ ! -e "$DEST/etc" ] || fail "--purge 应当删掉配置目录"

echo "install.sh / uninstall.sh:假根目录测试全过"
