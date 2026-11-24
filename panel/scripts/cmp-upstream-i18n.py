#!/usr/bin/env python3
"""把上游 panel/dist 的语言包与面板源码 panel/src/i18n 逐键比对。

用法(在仓库根目录):
    git show origin/catch-up-upstream:panel/dist/assets/en-XXXX.js.gz > /tmp/en.js.gz
    gunzip -c /tmp/en.js.gz > /tmp/en.js
    python panel/scripts/cmp-upstream-i18n.py /tmp/en.js /tmp/zh.js /tmp/zh-tw.js

参数顺序固定为 英文 / 简体中文 / 繁体中文 三份上游语言包,与 panel/src/i18n 的
en.ts / zh.ts / zh-tw.ts 对应。输出:每种语言的键数、main 缺的键、main 多的键、
键值不同的键、以及三份语言包之间键集是否一致。
"""

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LOCAL = {
    "en": ROOT / "src/i18n/en.ts",
    "zh": ROOT / "src/i18n/zh.ts",
    "zh-tw": ROOT / "src/i18n/zh-tw.ts",
}


def parse_object_literal(src: str) -> dict:
    """从 JS / TS 源码里取第一个对象字面量的键值(值保留源码文本)。

    只处理这里会遇到的形态:标识符键与引号键、字符串值、模板串值、
    一层嵌套对象值。返回 dict:键 -> 值(字符串值时是未转义后的文本,
    嵌套对象时是 dict)。
    """
    # 语言包都是 const X = { ... }(可能带类型标注),从赋值号后面那个 '{' 开始
    m = re.search(r"=\s*\{", src)
    if not m:
        raise SystemExit("找不到对象字面量的起始 '{'")
    start = m.end() - 1

    def parse(start_idx, want_depth=1):
        obj = {}
        i = start_idx + 1
        n = len(src)
        while i < n:
            # 跳过空白与注释
            while i < n and src[i] in " \t\r\n,":
                i += 1
            if i < n and src[i] == "/" and i + 1 < n and src[i + 1] == "/":
                while i < n and src[i] != "\n":
                    i += 1
                continue
            if i < n and src[i] == "/" and i + 1 < n and src[i + 1] == "*":
                i += 2
                while i + 1 < n and not (src[i] == "*" and src[i + 1] == "/"):
                    i += 1
                i += 2
                continue
            if i >= n or src[i] == "}":
                return obj, i + 1
            # 读键
            if src[i] in "\"'":
                q = src[i]
                j = i + 1
                buf = []
                while j < n:
                    if src[j] == "\\":
                        buf.append(src[j : j + 2])
                        j += 2
                        continue
                    if src[j] == q:
                        break
                    buf.append(src[j])
                    j += 1
                key = unescape("".join(buf))
                i = j + 1
            else:
                m = re.match(r"[A-Za-z_$][\w$]*", src[i:])
                if not m:
                    raise SystemExit(f"无法解析键,位置 {i}: {src[i:i+40]!r}")
                key = m.group(0)
                i += len(key)
            # 跳过冒号
            while i < n and src[i] in " \t\r\n":
                i += 1
            if i >= n or src[i] != ":":
                raise SystemExit(f"键 {key} 后没有冒号,位置 {i}: {src[i:i+40]!r}")
            i += 1
            while i < n and src[i] in " \t\r\n":
                i += 1
            # 读值
            if i < n and src[i] == "{":
                val, i = parse(i)
                obj[key] = val
            elif i < n and src[i] in "\"'`":
                q = src[i]
                j = i + 1
                buf = []
                while j < n:
                    if q != "`" and src[j] == "\\":
                        buf.append(src[j : j + 2])
                        j += 2
                        continue
                    if src[j] == q:
                        break
                    buf.append(src[j])
                    j += 1
                obj[key] = unescape("".join(buf))
                i = j + 1
            else:
                raise SystemExit(f"键 {key} 的值不是字符串或对象,位置 {i}: {src[i:i+60]!r}")
        return obj, i

    obj, _ = parse(start)
    return obj


def unescape(s: str) -> str:
    out = []
    i = 0
    while i < len(s):
        if s[i] == "\\" and i + 1 < len(s):
            c = s[i + 1]
            mapping = {"n": "\n", "t": "\t", "r": "\r", "b": "\b", "f": "\f", "v": "\v"}
            if c in mapping:
                out.append(mapping[c])
                i += 2
                continue
            if c == "u":
                if s[i + 2 : i + 3] == "{":
                    end = s.index("}", i + 2)
                    out.append(chr(int(s[i + 3 : end], 16)))
                    i = end + 1
                    continue
                out.append(chr(int(s[i + 2 : i + 6], 16)))
                i += 6
                continue
            out.append(c)
            i += 2
            continue
        out.append(s[i])
        i += 1
    return "".join(out)


def flatten(obj: dict, prefix: str = "") -> dict:
    flat = {}
    for k, v in obj.items():
        name = f"{prefix}{k}"
        if isinstance(v, dict):
            flat.update(flatten(v, name + "."))
        else:
            flat[name] = v
    return flat


def main() -> None:
    if len(sys.argv) != 4:
        raise SystemExit(__doc__)
    ups = {}
    for name, path in zip(("en", "zh", "zh-tw"), sys.argv[1:]):
        ups[name] = flatten(parse_object_literal(Path(path).read_text(encoding="utf-8")))
    locs = {name: flatten(parse_object_literal(p.read_text(encoding="utf-8"))) for name, p in LOCAL.items()}

    for name in ("en", "zh", "zh-tw"):
        up, loc = ups[name], locs[name]
        print(f"== {name}: 上游 {len(up)} 键,main {len(loc)} 键")
        missing = [k for k in up if k not in loc]
        extra = [k for k in loc if k not in up]
        print(f"   缺 {len(missing)}:", missing)
        print(f"   多 {len(extra)}:", extra)
        diff = [k for k in up if k in loc and up[k] != loc[k]]
        print(f"   值不同 {len(diff)}:")
        for k in diff:
            print(f"     {k}\n       上游: {up[k]!r}\n       main: {loc[k]!r}")
        order = [k for k in up if k in loc]
        print(f"   共有的键里 main 顺序完全按上游: {order == [k for k in loc if k in up]}")

    print()
    for name in ("en", "zh", "zh-tw"):
        print(f"{name}: {len(ups[name])} 键")
    base = set(ups["en"])
    for name in ("zh", "zh-tw"):
        print(f"  上游 {name} 相对 en 缺: {sorted(base - set(ups[name]))}")
        print(f"  上游 {name} 相对 en 多: {sorted(set(ups[name]) - base)}")


if __name__ == "__main__":
    main()
