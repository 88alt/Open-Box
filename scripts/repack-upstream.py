#!/usr/bin/env python3
"""把上游(默认 liandu2024/Open-Box)同版本的 release 重新封装成本仓库的 release。

上游自 v0.1.463 起面板后端是纯 Go 静态二进制(meta.json 的 panelRuntime = go-static),
没有 Node、没有 panel/server 明文后端,也没有可以在本仓库里重新编译的东西。所以发布不再
"自己打包",而是:

  - bin/openbox-panel、sing-box、mihomo、Geo 数据:上游的文件,按 components.json 里的
    sha256 校验后原样使用(app 组件里除 bin/ 以外的文本文件见下一条);
  - app 组件里 openwrt/、debian/、panel/、update.sh、uninstall.sh:把上游仓库地址换成本仓库
    (与 Sync upstream 的范围一致;COPYRIGHT.md 的版权出处声明不动),重新打成 app 组件;
    二进制里写死的上游地址换不了,面板自带的"检查更新"仍会看上游 Releases;
  - 完整安装包 open-box-linux-<arch>.tar.gz:新 app + 上游 kernel + 上游 geo 组合而成,
    结构与上游完整包相同(Docker 构建脚本按它校验);
  - 组件清单 components.json:沿用上游的 schema:2 清单,只改 app 组件的 sha256 / size
    (路由器上的 update.sh 把它交给 bin/openbox-panel 的 component-update 校验)。

用法:
  python3 scripts/repack-upstream.py build <x64|arm64> <outdir>
  python3 scripts/repack-upstream.py sources <outdir>     # 取回 GPL 对应源码包

版本必须与 .upstream-synced(Sync upstream 同步到的上游版本)一致:面板二进制和前端、
openwrt 脚本是同一个上游版本,不能混搭。

环境变量:
  OPENBOX_VERSION       本次发布的版本号(CI 里是 tag),必须等于 .upstream-synced
  OPENBOX_KERNEL_REPO   上游仓库,默认 liandu2024/Open-Box
  OPENBOX_REPO          本仓库,默认 timeflysoon/Open-Box
"""
import hashlib
import io
import json
import os
import re
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

UPSTREAM = os.environ.get('OPENBOX_KERNEL_REPO') or 'liandu2024/Open-Box'
OWN = os.environ.get('OPENBOX_REPO') or 'timeflysoon/Open-Box'
ROOT = Path(__file__).resolve().parent.parent
ARCHES = ('x64', 'arm64')
SAFE = re.compile(r'[0-9A-Za-z._-]+')
# 只在这些路径里换仓库地址(与 Sync upstream 的范围一致)。COPYRIGHT.md / LICENSE 里的"官方仓库"是
# 版权与出处声明,不能改成本仓库;bin/ 是二进制,换不了。
REWRITE_SCOPE = ('openwrt/', 'debian/', 'panel/', 'update.sh', 'uninstall.sh')


def log(msg):
    print('[repack] ' + msg, file=sys.stderr, flush=True)


def die(msg):
    raise SystemExit('[repack] 错误:' + msg)


def sha_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha_file(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def fetch(url, retries=4):
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, headers={'User-Agent': 'Open-Box-repack'})
            with urllib.request.urlopen(req, timeout=120) as response:
                return response.read()
        except urllib.error.HTTPError as exc:
            if exc.code == 404:
                raise
            last = exc
        except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
            last = exc
        time.sleep(2 * (attempt + 1))
    die(f'下载失败:{url}({last})')


def upstream_tag():
    """上游版本 = .upstream-synced,并且必须等于本次发布的版本号。"""
    synced = (ROOT / '.upstream-synced').read_text().strip() if (ROOT / '.upstream-synced').is_file() else ''
    if not re.fullmatch(r'v[0-9][0-9A-Za-z._-]*', synced):
        die('读不到 .upstream-synced,请先在 catch-up-upstream 分支运行 Sync upstream')
    version = os.environ.get('OPENBOX_VERSION', '')
    if version and version != synced:
        die(f'发布版本 {version} 与仓库同步到的上游版本 {synced} 不一致。'
            '纯 Go 面板二进制与上游版本绑定,tag 必须等于 .upstream-synced;'
            '要发别的版本,先用 Sync upstream 同步到那个上游版本。')
    return synced


def load_manifest(tag, arch):
    base = f'https://github.com/{UPSTREAM}/releases/download/{tag}'
    name = f'open-box-{tag}-linux-{arch}-components.json'
    raw = fetch(f'{base}/{name}')
    want = fetch(f'{base}/{name}.sha256').decode().split()[0]
    if sha_bytes(raw) != want:
        die(f'{name} 的 sha256 与 .sha256 文件不符')
    manifest = json.loads(raw)
    if manifest.get('schema') != 2 or manifest.get('version') != tag or manifest.get('arch') != arch:
        die(f'{name} 不是预期的 schema:2 / {tag} / {arch} 清单')
    comps = manifest.get('components', {})
    for need in ('app', 'kernel', 'geo'):
        if need not in comps:
            die(f'组件清单里没有 {need}')
    for kind, comp in comps.items():
        asset = str(comp.get('asset', ''))
        if not re.fullmatch(r'open-box-[0-9A-Za-z._-]+\.tar\.gz', asset) or not re.fullmatch(r'[0-9a-f]{64}', str(comp.get('sha256', ''))):
            die(f'组件 {kind} 的资产名或 sha256 异常')
    return manifest


def download_component(tag, comp, cache):
    asset, want = comp['asset'], comp['sha256']
    target = cache / asset
    data = fetch(f'https://github.com/{UPSTREAM}/releases/download/{tag}/{asset}')
    if sha_bytes(data) != want:
        die(f'{asset} 的 sha256 与组件清单不符')
    if comp.get('size') is not None and len(data) != comp['size']:
        die(f'{asset} 的大小与组件清单不符')
    target.write_bytes(data)
    return target


def normalize(member):
    member.uid = member.gid = 0
    member.uname = member.gname = 'root'
    member.pax_headers = {}
    return member


def rewrite_app(src, dst):
    """REWRITE_SCOPE 内的文件里,把上游仓库地址换成本仓库。返回 (改动的文件列表, {文件名: 内容})。"""
    old, new = UPSTREAM.encode(), OWN.encode()
    changed, kept = [], {}
    with tarfile.open(src, 'r:gz') as tin, tarfile.open(dst, 'w:gz', format=tarfile.PAX_FORMAT, compresslevel=9) as tout:
        for member in tin:
            data = None
            if member.isfile():
                data = tin.extractfile(member).read()
                if member.name.startswith(REWRITE_SCOPE):
                    if old in data:
                        data = data.replace(old, new)
                        member.size = len(data)
                        changed.append(member.name)
                if member.name in ('update.sh', 'uninstall.sh', 'meta.json'):
                    kept[member.name] = data
            tout.addfile(normalize(member), io.BytesIO(data) if data is not None else None)
    return changed, kept


def check_repo_scripts(kept):
    """仓库里的 scripts/update.sh、uninstall.sh 会作为 Release 附件单独发布,路由器按它们的 sha256 校验;
    必须与包里的同名脚本一致,否则说明仓库没同步到这个上游版本。"""
    for name in ('update.sh', 'uninstall.sh'):
        repo_file = ROOT / 'scripts' / name
        if not repo_file.is_file() or repo_file.read_bytes() != kept.get(name):
            die(f'scripts/{name} 与上游包里的 {name}(换仓库地址后)不一致,请先运行 Sync upstream')


def add_tar(out, src, seen):
    with tarfile.open(src, 'r:gz') as tin:
        for member in tin:
            if member.name in seen:
                if member.isdir():
                    continue
                die(f'完整包里出现重复文件:{member.name}')
            seen.add(member.name)
            data = tin.extractfile(member).read() if member.isfile() else None
            out.addfile(normalize(member), io.BytesIO(data) if data is not None else None)


def write_sha(path):
    path = Path(path)
    (path.parent / (path.name + '.sha256')).write_text(f'{sha_file(path)}  {path.name}\n')


def build(arch, outdir):
    if arch not in ARCHES:
        die(f'架构应为 x64 或 arm64,收到:{arch}')
    tag = upstream_tag()
    out = Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    log(f'重新封装上游 {UPSTREAM}@{tag}(linux-{arch})')
    manifest = load_manifest(tag, arch)
    comps = manifest['components']

    with tempfile.TemporaryDirectory(prefix='open-box-repack.') as tmp:
        cache = Path(tmp)
        paths = {kind: download_component(tag, comp, cache) for kind, comp in comps.items()}

        # ---- app:换仓库地址后重新打包 ----
        app_asset = comps['app']['asset']
        app_out = out / app_asset
        changed, kept = rewrite_app(paths['app'], app_out)
        check_repo_scripts(kept)
        leftover = []
        with tarfile.open(app_out, 'r:gz') as tin:
            for member in tin:
                if member.isfile() and member.name.startswith(REWRITE_SCOPE) \
                        and UPSTREAM.encode() in tin.extractfile(member).read():
                    leftover.append(member.name)
        if leftover:
            die('app 组件里仍有指向上游的残留:' + ', '.join(leftover[:10]))
        with tarfile.open(app_out, 'r:gz') as tin:
            binary = hashlib.sha256(tin.extractfile('bin/openbox-panel').read()).hexdigest()
        want_bin = comps['app'].get('files', {}).get('bin/openbox-panel')
        if want_bin and binary != want_bin:
            die('bin/openbox-panel 的 sha256 与组件清单不符')
        log(f'app 组件:{len(changed)} 个文件换了仓库地址;bin/openbox-panel 与上游一致({binary[:12]})')

        # ---- 其余组件:上游文件原样放进 Release ----
        for kind, comp in comps.items():
            if kind != 'app':
                (out / comp['asset']).write_bytes(paths[kind].read_bytes())

        # ---- 新清单:只改 app 的 sha256 / size ----
        new_manifest = json.loads(json.dumps(manifest))
        new_manifest['components']['app']['sha256'] = sha_file(app_out)
        new_manifest['components']['app']['size'] = app_out.stat().st_size
        for comp in new_manifest['components'].values():
            write_sha(out / comp['asset']) if (out / comp['asset']).is_file() else die('缺少组件文件 ' + comp['asset'])
        versioned = out / f'open-box-{tag}-linux-{arch}-components.json'
        versioned.write_text(json.dumps(new_manifest, indent=2) + '\n')
        write_sha(versioned)
        stable = out / f'open-box-linux-{arch}-components.json'
        stable.write_bytes(versioned.read_bytes())
        write_sha(stable)

        # ---- 完整安装包:app + kernel + geo + kernel-mihomo(与上游完整包同结构)----
        # v0.1.469 起纯 Go 版内置 sing-box / mihomo 双核,包里自带的 component-update verify 会校验 kernel-mihomo,
        # 完整包里缺它就会报「组件校验失败: kernel-mihomo」。清单里有 kernel-mihomo 就一并放进去。
        full_v = out / f'open-box-{tag}-linux-{arch}.tar.gz'
        seen = set()
        with tarfile.open(full_v, 'w:gz', format=tarfile.PAX_FORMAT, compresslevel=9) as tout:
            parts = [app_out, paths['kernel'], paths['geo']]
            if 'kernel-mihomo' in paths:
                parts.append(paths['kernel-mihomo'])
            for src in parts:
                add_tar(tout, src, seen)
        full_s = out / f'open-box-linux-{arch}.tar.gz'
        full_s.write_bytes(full_v.read_bytes())
        write_sha(full_v)
        write_sha(full_s)
        for needed in ('bin/openbox-panel', 'bin/sing-box', 'meta.json', 'panel/dist/index.html',
                       'panel/server/resources/geodata/manifest.json'):
            if needed not in seen:
                die(f'完整包里缺少 {needed}')
        meta = json.loads(kept['meta.json'])
        if meta.get('panelRuntime') != 'go-static' or meta.get('arch') != arch or meta.get('version') != tag:
            die('meta.json 的 panelRuntime / arch / version 与预期不符')
        log(f'完成:{full_v.name}({full_v.stat().st_size // 1024 // 1024} MB),面板构建 {meta.get("panelBuildId")}')


def sources(outdir):
    """取回随二进制分发所需的对应源码(GPL):sing-box 必须有,mihomo / Geo 源取不到只警告。"""
    tag = upstream_tag()
    out = Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    manifest = load_manifest(tag, 'x64')
    comps = manifest['components']
    wanted = [(f'sing-box-{comps["kernel"]["version"]}-source.tar.gz', True)]
    if 'kernel-mihomo' in comps:
        wanted.append((f'mihomo-{comps["kernel-mihomo"]["version"]}-source.tar.gz', False))
    wanted.append((f'geodata-{comps["geo"]["version"]}-sources.tar.xz', False))
    base = f'https://github.com/{UPSTREAM}/releases/download/{tag}'
    for name, required in wanted:
        if not SAFE.fullmatch(name):
            die(f'文件名异常:{name}')
        try:
            data = fetch(f'{base}/{name}')
            want = fetch(f'{base}/{name}.sha256').decode().split()[0]
        except urllib.error.HTTPError:
            if required:
                die(f'取不到 {name}(上游 {tag});没有对应源码不能分发这个二进制')
            print(f'::warning::上游 {tag} 里没有 {name},本次不附带', flush=True)
            continue
        if sha_bytes(data) != want:
            die(f'{name} 的 sha256 校验失败')
        (out / name).write_bytes(data)
        (out / (name + '.sha256')).write_text(f'{want}  {name}\n')
        log(f'源码:{name}')


def main(argv):
    if len(argv) == 4 and argv[1] == 'build':
        build(argv[2], argv[3])
    elif len(argv) == 3 and argv[1] == 'sources':
        sources(argv[2])
    else:
        print(__doc__, file=sys.stderr)
        raise SystemExit(2)


if __name__ == '__main__':
    main(sys.argv)
