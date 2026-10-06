# -*- coding: utf-8 -*-
"""make_release.py —— 用本机 git 凭据自动创建 GitHub Release 并上传 dist zip。

用法：python tools/make_release.py <zip路径> <tag> <标题> <说明md路径>
说明：token 通过 `git credential fill` 现取现用，不写入任何文件。
"""
import json
import os
import subprocess
import sys

# **stdout 显式设 UTF-8。**
#
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 `print('  ⚠ …')` 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
# recovery_sandbox.py 实测踩过：评测一行都没跑就崩在这里。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import urllib.request

REPO = "hrui83771-gif/EnvKit"


def get_token():
    p = subprocess.run(["git", "credential", "fill"], input="protocol=https\nhost=github.com\n\n",
                       capture_output=True, text=True)
    for line in p.stdout.splitlines():
        if line.startswith("password="):
            return line.split("=", 1)[1]
    sys.exit("本机没有可用的 GitHub 凭据")


def api(url, token, data=None, headers=None, method=None):
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("User-Agent", "make-release")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req) as r:
            body = r.read()
            return r.status, (json.loads(body) if body and body[:1] in (b"{", b"[") else body)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


def main():
    zip_path, tag, title, notes_path = sys.argv[1:5]
    if not os.path.exists(zip_path):
        sys.exit("zip 不存在: " + zip_path)
    notes = open(notes_path, encoding="utf-8").read()
    token = get_token()

    # 已有同 tag release 则复用
    st, old = api("https://api.github.com/repos/%s/releases/tags/%s" % (REPO, tag), token)
    if st == 200:
        rel = old
        print("复用已有 release:", rel["html_url"])
    else:
        st, rel = api("https://api.github.com/repos/%s/releases" % REPO, token,
                      data=json.dumps({"tag_name": tag, "name": title, "body": notes,
                                       "target_commitish": "main", "draft": False,
                                       "prerelease": False}).encode("utf-8"),
                      headers={"Content-Type": "application/json"})
        if st not in (201, 200):
            print("创建 release 失败:", st, rel)
            sys.exit(1)
        print("release 已创建:", rel["html_url"])

    asset_name = os.path.basename(zip_path)
    for a in rel.get("assets", []):
        if a["name"] == asset_name:
            print("资产已存在，跳过上传:", a["browser_download_url"])
            return
    size = os.path.getsize(zip_path)
    st, up = api("https://uploads.github.com/repos/%s/releases/%s/assets?name=%s"
                 % (REPO, rel["id"], asset_name), token,
                 data=open(zip_path, "rb").read(),
                 headers={"Content-Type": "application/zip"})
    if st == 201:
        print("资产上传成功（%.1f MB）:" % (size / 1048576), up["browser_download_url"])
    else:
        print("上传失败:", st, up)
        sys.exit(1)


if __name__ == "__main__":
    main()
