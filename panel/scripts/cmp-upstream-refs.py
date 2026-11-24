#!/usr/bin/env python3
"""比对「键引用」:上游 panel/dist 与面板源码 panel/src 各自引用了哪些 i18n 键。

用法(在仓库根目录;先按 cmp-upstream-i18n.py 的说明解压三份语言包):
    python panel/scripts/cmp-upstream-refs.py <上游 dist 的 assets 目录> <上游英文语言包.js>

键集以上游语言包为准。对每个键统计两侧的出现次数:上游引用了而 main 没引用、
main 引用了而上游没引用的,分别列出,便于按例外 A / B / C 筛掉。

注意两类虚警(脚本按「带引号的字面量」计数,分辨不出来):
  - 第三方打包块里的同名字符串:已直接跳过 vendor-* / vuedraggable.* 文件。
  - Vue 模板里的属性写法 `v-if="manageHiddenGroup"`:属性名和值是同一个标识符时,
    正则会把值两侧的引号当成字面量。这类「main 引用、上游没引用」要回到源码确认
    是不是 store 里的设置项(例如 manageHiddenGroup / customIcon),不是 i18n 引用。
"""

import importlib.util
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
EXCLUDE_PREFIX = (
    "businessOverview",
    "businessPreference",
    "businessSettingsHelp",
    "businessReliabilityHalfLife",
    "businessPerformanceTtl",
    "bandwidthHelpCurrentBusiness",
)

spec = importlib.util.spec_from_file_location("cmp_i18n", ROOT / "scripts" / "cmp-upstream-i18n.py")
cmp_i18n = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cmp_i18n)


def count_in(texts, keys):
    """单趟扫一遍文本,统计键作为「带引号的字面量」出现的次数。

    只数带引号的写法:打包产物里同名但没引号的标识符多来自第三方库(例如 Vue 组件自己的 nodeType
    属性、vuedraggable 里的 DOM nodeType),按裸标识符数会把它们算成 i18n 引用。
    """
    wanted = set(keys)
    counts = {k: 0 for k in keys}
    for text in texts:
        for m in re.finditer(r"""["']([A-Za-z_$][\w$]*)["']""", text):
            tok = m.group(1)
            if tok in wanted:
                counts[tok] += 1
    return counts


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    dist_dir = Path(sys.argv[1])
    keys = list(cmp_i18n.flatten(cmp_i18n.parse_object_literal(Path(sys.argv[2]).read_text(encoding="utf-8"))))
    upstream_js = [
        p
        for p in sorted(dist_dir.glob("*.js"))
        if not re.match(r"^(en|zh|zh-tw)-[A-Za-z0-9_-]+\.js$", p.name)
        # 第三方打包块里的同名字符串不算上游引用(例如 vue-i18n 的 scope:"global"、
        # core-js 的 mode:"global"、vuedraggable 里的 "global")。
        and not p.name.startswith(("vendor-", "vuedraggable."))
    ]
    local_files = [
        p
        for p in (ROOT / "src").rglob("*")
        # 一定要带上 .tsx / .jsx:面板里有几个组件是 TSX 写的(侧边栏 LogsCtrl、连接卡片),
        # 漏掉它们会把「main 已经引用了的键」误报成缺口。
        if p.suffix in {".vue", ".ts", ".tsx", ".js", ".jsx"} and "i18n" not in p.parts
    ]

    def read_all(paths):
        out = []
        for p in paths:
            try:
                out.append(p.read_text(encoding="utf-8", errors="replace"))
            except OSError:
                pass
        return out

    up = count_in(read_all(upstream_js), keys)
    loc = count_in(read_all(local_files), keys)
    print(f"上游 dist: {len(upstream_js)} 个 js;main src: {len(local_files)} 个文件")
    missing_ref = [k for k in keys if up[k] and not loc[k]]
    extra_ref = [k for k in keys if loc[k] and not up[k]]
    print(f"\n上游引用、main 没引用({len(missing_ref)}):")
    for k in missing_ref:
        tag = " [例外C]" if k.startswith(EXCLUDE_PREFIX) else ""
        print(f"  {k} (上游 {up[k]} 次){tag}")
    print(f"\nmain 引用、上游没引用({len(extra_ref)}):")
    for k in extra_ref:
        print(f"  {k} (main {loc[k]} 次)")
    both_none = [k for k in keys if not up[k] and not loc[k]]
    print(f"\n两侧都没引用({len(both_none)}):")
    print("  " + " ".join(both_none))


if __name__ == "__main__":
    main()
