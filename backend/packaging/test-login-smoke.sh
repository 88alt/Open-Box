#!/bin/sh
# 登录冒烟测试(发布前必跑):在干净环境里验证「首次设置密码 → 登录 → 改密 → 用新密码重登」
# 这条链路能走通才算可发布。
#
# 为什么要有它:开发机上的 data/state/auth.json 会被打进包,装到干净机器上时
# 「默认密码」根本不是用户以为的那个,表现为「密码不对」。在干净目录里跑一遍,
# 这类问题会在发布前暴露,而不是到用户手上才发现。
#
# 用法(在 backend/ 下,先用 GOOS=linux go build 出二进制,或本机直接跑):
#   sh packaging/test-login-smoke.sh <openbox-backend 可执行文件>
#
# 全程只用临时目录、只监听 127.0.0.1 的随机端口,不碰任何现有安装。
set -eu

BIN=${1:-}
[ -n "$BIN" ] || { echo "用法: $0 <openbox-backend 可执行文件>" >&2; exit 2; }
[ -x "$BIN" ] || { echo "失败:找不到可执行文件 $BIN" >&2; exit 2; }

BIN=$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")
WORK=$(mktemp -d)
PORT=${SMOKE_PORT:-23196}
BASE="http://127.0.0.1:$PORT"
PID=""

cleanup() {
	[ -n "$PID" ] && kill "$PID" 2>/dev/null || true
	[ -n "$PID" ] && wait "$PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fail() {
	echo "失败:$*" >&2
	echo "--- 后端日志 ---" >&2
	tail -30 "$WORK/log.txt" 2>/dev/null >&2 || true
	exit 1
}

# 极简 JSON 取值:够用于 {"ok":true,...} 这种扁平对象
jget() {
	grep -o "\"$2\":[^,}]*" "$1" | head -1 | cut -d: -f2- | tr -d '"' || true
}

echo "== 1. 在干净目录启动 =="
mkdir -p "$WORK/data" "$WORK/etc"
: > "$WORK/log.txt"
"$BIN" \
	-listen "127.0.0.1:$PORT" \
	-config-dir "$WORK/etc" \
	-data-dir "$WORK/data" \
	-geo-dir "$WORK/geo" \
	-core /nonexistent-core \
	-sing-box /nonexistent-sing-box \
	-dns-query-log off \
	>"$WORK/log.txt" 2>&1 &
PID=$!

# 等端口起来(最多 15 秒)
i=0
while [ $i -lt 30 ]; do
	if curl -s -m 2 "$BASE/api/auth/status" >"$WORK/st.json" 2>/dev/null; then break; fi
	i=$((i + 1))
	sleep 0.5
done
[ -s "$WORK/st.json" ] || fail "后端没起来"

# 关键断言:干净目录里必须「还没设密码」
setpwd=$(jget "$WORK/st.json" passwordSet)
[ "$setpwd" = "false" ] || fail "干净目录里 passwordSet 应为 false,实际 $setpwd(包或目录里带了旧 auth.json?)"
echo "   OK 干净环境 passwordSet=false(包里没带 auth.json)"

echo "== 2. 首次设置密码 =="
curl -s -m 5 -X POST "$BASE/api/auth/setup" -H "Content-Type: application/json" \
	-d '{"password":"smoke-test-1111"}' -c "$WORK/ck.txt" >"$WORK/setup.json" 2>/dev/null || fail "setup 请求失败"
ok=$(jget "$WORK/setup.json" ok)
[ "$ok" = "true" ] || fail "首次设置密码失败:$(cat "$WORK/setup.json")"
echo "   OK 首次设置成功,并拿到会话"

echo "== 3. 带会话访问受保护接口 =="
code=$(curl -s -o "$WORK/prot.json" -w '%{http_code}' -m 5 -b "$WORK/ck.txt" "$BASE/api/openbox/service/status" 2>/dev/null)
[ "$code" = "200" ] || fail "受保护接口应 200,实际 $code:$(cat "$WORK/prot.json" 2>/dev/null)"
echo "   OK 会话可用(200)"

echo "== 4. 退出后用原密码重新登录 =="
curl -s -m 5 -X POST "$BASE/api/auth/logout" -b "$WORK/ck.txt" >/dev/null 2>&1 || true
rm -f "$WORK/ck.txt"
curl -s -m 5 -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
	-d '{"password":"smoke-test-1111"}' -c "$WORK/ck.txt" >"$WORK/login.json" 2>/dev/null || fail "登录请求失败"
ok=$(jget "$WORK/login.json" ok)
[ "$ok" = "true" ] || fail "用刚设的密码登录失败:$(cat "$WORK/login.json")"
echo "   OK 原密码登录成功"

echo "== 5. 改密码 =="
curl -s -m 5 -X POST "$BASE/api/auth/change-password" -H "Content-Type: application/json" \
	-b "$WORK/ck.txt" -d '{"currentPassword":"smoke-test-1111","newPassword":"smoke-test-2222"}' >"$WORK/chg.json" 2>/dev/null || fail "改密请求失败"
ok=$(jget "$WORK/chg.json" ok)
if [ "$ok" != "true" ]; then
	# 有的实现把改密放在 PUT
	curl -s -m 5 -X PUT "$BASE/api/auth/change-password" -H "Content-Type: application/json" \
		-b "$WORK/ck.txt" -d '{"currentPassword":"smoke-test-1111","newPassword":"smoke-test-2222"}' >"$WORK/chg.json" 2>/dev/null || true
	ok=$(jget "$WORK/chg.json" ok)
fi
[ "$ok" = "true" ] || fail "改密失败:$(cat "$WORK/chg.json")"
echo "   OK 改密成功"

echo "== 6. 旧密码必须失败、新密码必须成功 =="
curl -s -m 5 -X POST "$BASE/api/auth/logout" -b "$WORK/ck.txt" >/dev/null 2>&1 || true
curl -s -m 5 -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
	-d '{"password":"smoke-test-1111"}' >"$WORK/old.json" 2>/dev/null || true
code=$(grep -o '"code":"[A-Z_]*"' "$WORK/old.json" 2>/dev/null | head -1 || true)
if [ -z "$code" ] && [ "$(jget "$WORK/old.json" ok)" = "true" ]; then
	fail "改密后旧密码仍能登录"
fi
echo "   OK 旧密码被拒($code)"

curl -s -m 5 -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
	-d '{"password":"smoke-test-2222"}' -c "$WORK/ck2.txt" >"$WORK/new.json" 2>/dev/null || fail "新密码登录请求失败"
ok=$(jget "$WORK/new.json" ok)
[ "$ok" = "true" ] || fail "新密码登录失败:$(cat "$WORK/new.json")"
echo "   OK 新密码登录成功"

echo "== 7. 重启后端后新密码仍然有效(落盘了) =="
kill "$PID" 2>/dev/null || true
wait "$PID" 2>/dev/null || true
: > "$WORK/log.txt"
"$BIN" \
	-listen "127.0.0.1:$PORT" \
	-config-dir "$WORK/etc" \
	-data-dir "$WORK/data" \
	-geo-dir "$WORK/geo" \
	-core /nonexistent-core \
	-sing-box /nonexistent-sing-box \
	-dns-query-log off \
	>"$WORK/log.txt" 2>&1 &
PID=$!
i=0
while [ $i -lt 30 ]; do
	if curl -s -m 2 "$BASE/api/auth/status" >/dev/null 2>&1; then break; fi
	i=$((i + 1))
	sleep 0.5
done
curl -s -m 5 -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
	-d '{"password":"smoke-test-2222"}' >"$WORK/re.json" 2>/dev/null || fail "重启后登录请求失败"
ok=$(jget "$WORK/re.json" ok)
[ "$ok" = "true" ] || fail "重启后用新密码登录失败:$(cat "$WORK/re.json")"
echo "   OK 重启后新密码仍有效"

echo
echo "登录冒烟全部通过:干净环境 → 设密码 → 登录 → 改密 → 旧密码失效 → 新密码可用 → 重启后仍有效"
