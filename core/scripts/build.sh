#!/bin/sh
# 构建 openbox-core。
#
# 用法: sh core/scripts/build.sh <amd64|arm64> <outdir> [index.mjs 路径]
#
# buildID:自研后端不校验(只要求 implementation=go-native、protocol=1),它只是个构建标识。
# 取法按顺序:环境变量 OPENBOX_CORE_BUILD_ID(64 位十六进制);第三个参数或 ../panel/server/index.mjs
# 存在时从里面读(上游 Node 的写法,仅给还要接上游 Node 的场合);都没有就用 core 源码(不含测试)的 sha256。
#
# 产出:
#   <outdir>/openbox-core      静态链接、去符号的 linux 二进制
#   <outdir>/core-build.json   protocol/implementation/buildID/goVersion/os/arch/sha256/size
#                              (不写 bindingSha256;键的顺序要和 hardening-manifest.json 的 core 一致)
set -eu

die() { echo "[core-build] 错误:$*" >&2; exit 1; }

[ "$#" -ge 2 ] || die "用法: $0 <amd64|arm64> <outdir> [index.mjs]"
case "$1" in
  amd64|x64) GOARCH=amd64 ;;
  arm64) GOARCH=arm64 ;;
  *) die "不支持的架构:$1" ;;
esac
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CORE=$(CDPATH= cd -- "$HERE/.." && pwd)
mkdir -p "$2"
OUT=$(CDPATH= cd -- "$2" && pwd)
INDEX="${3:-$CORE/../panel/server/index.mjs}"

ID="${OPENBOX_CORE_BUILD_ID:-}"
if [ -z "$ID" ] && [ ! -f "$INDEX" ]; then
  # main 上没有 panel/server:按源码内容生成,同一份源码得到同一个值
  ID=$(cd "$CORE" && find cmd internal go.mod -type f \( -name '*.go' -o -name go.mod \) ! -name '*_test.go' | LC_ALL=C sort | python3 -c '
import hashlib, sys
h = hashlib.sha256()
for name in sys.stdin.read().split():
    h.update(name.encode() + b"\0" + open(name, "rb").read() + b"\0")
print(h.hexdigest())
')
elif [ -z "$ID" ]; then
  ID=$(python3 - "$INDEX" <<'PY'
import re, sys
t = open(sys.argv[1], encoding='utf-8', errors='replace').read()
# 形如  Re=qE(GE,"<64位hex>")  的核心桥接初始化
m = re.findall(r'[A-Za-z_$][\w$]*=[A-Za-z_$][\w$]*\([A-Za-z_$][\w$]*,"([0-9a-f]{64})"\)', t)
print(m[0] if len(set(m)) == 1 else '')
PY
)
fi
case "$ID" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) [ "${#ID}" = 64 ] || die "buildID 不是 64 位十六进制:$ID" ;;
  *) die "没能确定 buildID(index.mjs 里的写法可能变了)" ;;
esac
echo "[core-build] buildID=$ID arch=$GOARCH"

cd "$CORE"
GOVER=$(go env GOVERSION | sed 's/^go//')
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -trimpath \
  -ldflags "-s -w -X github.com/timeflysoon/Open-Box/core/internal/protocol.BuildID=$ID" \
  -o "$OUT/openbox-core" ./cmd/openbox-core
chmod +x "$OUT/openbox-core"

python3 - "$OUT" "$ID" "$GOARCH" "$GOVER" <<'PY'
import hashlib, json, sys
out, bid, arch, gover = sys.argv[1:5]
b = open(out + '/openbox-core', 'rb').read()
core = {
    'protocol': 1, 'implementation': 'go-native', 'buildID': bid, 'goVersion': gover,
    'os': 'linux', 'arch': arch, 'sha256': hashlib.sha256(b).hexdigest(), 'size': len(b),
}
open(out + '/core-build.json', 'w', encoding='utf-8', newline='\n').write(json.dumps(core, indent=2) + '\n')
print('[core-build] sha256=' + core['sha256'], 'size=%d' % len(b))
PY
