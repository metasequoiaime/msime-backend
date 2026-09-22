#!/usr/bin/env python3
"""下载并校验固定的公共词库；不覆盖已有文件或读取客户端用户词库。"""
import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]

# GitHub 的下载偶尔整段时间返回 504，2026-09-14 那次持续了二十来分钟，同一个窗口里三个仓库的 CI 全
# 挂在各自的下载上。一次失败就退出等于把 CI 的成败绑在对方那几分钟的可用性上，而这里下载的内容有
# 摘要校验，重试不会引入坏数据。
RETRY_STATUS = frozenset({408, 425, 429, 500, 502, 503, 504})
ATTEMPTS = 5


def open_with_retry(request):
    for attempt in range(1, ATTEMPTS + 1):
        try:
            return urllib.request.urlopen(request, timeout=60)
        except urllib.error.HTTPError as error:
            # 404、403 这些重试多少次都是同一个答案，立刻失败比等五轮退避有用。
            if error.code not in RETRY_STATUS or attempt == ATTEMPTS:
                raise
            reason = f'HTTP {error.code}'
        except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
            if attempt == ATTEMPTS:
                raise
            reason = str(error)
        delay = 2 ** attempt
        print(f'下载失败（{reason}），{delay}s 后重试（第 {attempt}/{ATTEMPTS - 1} 次）', flush=True)
        time.sleep(delay)

def download(destination, native_build=None):
    lock = json.loads((ROOT/'native/resources.lock.json').read_text())
    destination.mkdir(parents=True, exist_ok=True)
    for name, digest in lock['assets'].items():
        target = destination/name
        if target.exists():
            if hashlib.sha256(target.read_bytes()).hexdigest() != digest:
                raise SystemExit(f'已有资源摘要不匹配：{name}；请选择新的资源目录')
            continue
        url = f'https://github.com/{lock["repository"]}/releases/download/{lock["tag"]}/{name}'
        request = urllib.request.Request(url, headers={'User-Agent': 'MSIME-Backend-resource-fetch'})
        with tempfile.NamedTemporaryFile(dir=destination, prefix='.download-', delete=False) as output:
            temporary = Path(output.name)
            try:
                with open_with_retry(request) as response:
                    digest_state = hashlib.sha256()
                    size = 0
                    while chunk := response.read(1024*1024):
                        size += len(chunk)
                        if size > 256*1024*1024:
                            raise ValueError('resource too large')
                        digest_state.update(chunk)
                        output.write(chunk)
                output.close()
                if digest_state.hexdigest() != digest:
                    raise ValueError(f'资源摘要不匹配：{name}')
                temporary.replace(target)
            finally:
                temporary.unlink(missing_ok=True)
    source_root = ROOT/'third_party/MSIME-Engine'
    for name, entry in lock.get('source_files', {}).items():
        source = source_root/entry['path']
        raw = source.read_bytes()
        if hashlib.sha256(raw).hexdigest() != entry['sha256']:
            raise SystemExit(f'固定源码资源摘要不匹配：{name}')
        target = destination/name
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.exists():
            if target.read_bytes() != raw:
                raise SystemExit(f'已有源码资源不匹配：{name}；请选择新的资源目录')
        else:
            target.write_bytes(raw)
    for group, source_root in [('third_party_files', ROOT), ('built_files', native_build)]:
        for name, entry in lock.get(group, {}).items():
            target = destination/name
            if target.exists():
                if hashlib.sha256(target.read_bytes()).hexdigest() != entry['sha256']:
                    raise SystemExit(f'已有资源摘要不匹配：{name}')
                continue
            if source_root is None:
                raise SystemExit('首次准备转换资源需要 --native-build 指向已编译目录')
            raw = (source_root/entry['path']).read_bytes()
            if hashlib.sha256(raw).hexdigest() != entry['sha256']:
                raise SystemExit(f'转换资源摘要不匹配：{name}')
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(raw)
    manifest = json.loads((destination/'dictionary-manifest.json').read_text())
    if manifest['source']['commit'] != lock['source_commit'] or manifest['format_version'] != 1:
        raise SystemExit('词库来源或格式不匹配')
    print('固定词库来源与 SHA-256 校验通过')

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('destination', type=Path)
    parser.add_argument('--native-build', type=Path)
    args = parser.parse_args()
    download(args.destination, args.native_build)
