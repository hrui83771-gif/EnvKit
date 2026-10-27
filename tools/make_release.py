# -*- coding: utf-8 -*-
"""make_release.py —— 自动创建 GitHub Release 并上传 dist zip。

用法：python tools/make_release.py <zip路径> <tag> <标题> <说明md路径>

token 来源（按优先级）：
  1. 环境变量 GITHUB_TOKEN / GH_TOKEN（临时 token，不落盘）
  2. `git credential fill` 现取（本机凭据管理器）

**为什么用 curl.exe 而不是 urllib**：本机所有 Python 请求都会走
`http://127.0.0.1:9570` 这个本地代理，而它的证书链在 Python 的 OpenSSL
下校验不过（`CERTIFICATE_VERIFY_FAILED: unable to get issuer certificate`），
同一时刻 curl（Windows 自带，走 Schannel + 系统证书库）却正常。
用 curl 不是为了少写代码，是为了让这个脚本在这台机器上真的能跑完。
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

REPO = "hrui83771-gif/EnvKit"
CURL = "curl.exe"


def get_token():
    # 优先用环境变量：本机凭据管理器里的 token 可能是别处流程留下的死令牌
    #（表现为 401 Bad credentials），现取的临时 token 放环境变量里更可控，
    # 也不落盘。环境变量没有才退回 git 凭据。
    for k in ("GITHUB_TOKEN", "GH_TOKEN"):
        v = os.environ.get(k, "").strip()
        if v:
            print("using token from $%s (len=%d)" % (k, len(v)))
            return v
    p = subprocess.run(["git", "credential", "fill"], input="protocol=https\nhost=github.com\n\n",
                       capture_output=True, text=True)
    for line in p.stdout.splitlines():
        if line.startswith("password="):
            return line.split("=", 1)[1]
    sys.exit("本机没有可用的 GitHub 凭据（也没设 GITHUB_TOKEN）")


def _curl(args, ctype=None):
    """跑一次 curl，返回 (status, body)。body 是 dict 时自动解析 JSON。"""
    cmd = [CURL, "-sS", "--max-time", "300", "-w", "\n%{http_code}",
           "-H", "Authorization: Bearer " + TOKEN,
           "-H", "Accept: application/vnd.github+json",
           "-H", "User-Agent: envkit-release"]
    if ctype:
        cmd += ["-H", "Content-Type: " + ctype]
    cmd += args
    r = subprocess.run(cmd, capture_output=True)
    raw = r.stdout.decode("utf-8", "replace")
    err = r.stderr.decode("utf-8", "replace").strip()
    if r.returncode != 0:
        return -1, ("curl 失败 rc=%d %s" % (r.returncode, err))
    head, _, code = raw.rpartition("\n")
    try:
        status = int(code.strip())
    except ValueError:
        return -1, raw[:300]
    body = head
    if body[:1] in ("{", "["):
        try:
            body = json.loads(body)
        except Exception:
            pass
    return status, body


TOKEN = ""


def api_get(url):
    return _curl([url])


def api_post_json(url, payload):
    return _curl(["-X", "POST", "--data-binary", json.dumps(payload), url],
                 ctype="application/json")


def upload_asset(upload_url, path):
    # 不手写 Content-Length：curl 对 --data-binary @file 会自己带上，
    # 另加一个反而可能被 GitHub 判成重复头。
    return _curl(["-X", "POST", "--data-binary", "@" + path, upload_url],
                 ctype="application/zip")


def main():
    global TOKEN
    if len(sys.argv) < 5:
        sys.exit("用法: python tools/make_release.py <zip> <tag> <标题> <说明md>")
    zip_path, tag, title, notes_path = sys.argv[1:5]
    if not os.path.exists(zip_path):
        sys.exit("zip 不存在: " + zip_path)
    notes = open(notes_path, encoding="utf-8").read()
    TOKEN = get_token()

    # 已有同 tag release 则复用
    st, old = api_get("https://api.github.com/repos/%s/releases/tags/%s" % (REPO, tag))
    if st == 200:
        rel = old
        print("复用已有 release:", rel["html_url"])
    else:
        st, rel = api_post_json("https://api.github.com/repos/%s/releases" % REPO,
                                {"tag_name": tag, "name": title, "body": notes,
                                 "target_commitish": "main", "draft": False,
                                 "prerelease": False})
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
    upload_url = ("https://uploads.github.com/repos/%s/releases/%s/assets?name=%s"
                  % (REPO, rel["id"], asset_name))
    st, up = upload_asset(upload_url, zip_path)
    if st == 201:
        print("资产上传成功（%.1f MB）:" % (size / 1048576), up["browser_download_url"])
    else:
        print("上传失败:", st, up)
        sys.exit(1)


if __name__ == "__main__":
    main()
