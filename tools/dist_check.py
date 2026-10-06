"""分发前验证：把 exe 拷到干净目录运行，确认内嵌默认值不含任何个人信息。"""
import sys

# **stdout 显式设 UTF-8。**
# Windows 上 PowerShell / 重定向给的是 GBK，
# 于是 print('  ⚠ …') 会抛 UnicodeEncodeError ——
# 崩在评测中途，看起来像「装置坏了」，而它只是控制台编码。
try:
    sys.stdout.reconfigure(encoding='utf-8')
except Exception:
    pass

import json, os, re, shutil, subprocess, sys, time, urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)  # tools/ 的上一级 = envkit 根目录
TMP = os.path.join(HERE, "_disttest")
EXE = os.path.join(ROOT, "EnvKit.exe")

# 敏感串从本机 config.json 动态收集（与 make_dist.py 同源），不再写死个人 IP
COMMON = {"root", "admin", "user", "localhost", "127.0.0.1", "0.0.0.0",
          "22", "3306", "20200", "5002", "deepseek", "123456", "utf8mb4"}


def secrets_from_config():
    path = os.path.join(ROOT, "config.json")
    out = ["dpapi:", "FarmTrace", "BCGD"]  # 固定哨兵：密文前缀与本机项目名
    if os.path.exists(path):
        cfg = json.load(open(path, encoding="utf-8"))

        def dig(d, expr):
            cur = d
            for k in expr.split("."):
                if not isinstance(cur, dict) or k not in cur:
                    return None
                cur = cur[k]
            return cur

        for expr in ["chain.ssh_host", "chain.ssh_user", "chain.ssh_password",
                     "chain.ssh_host_key", "projects.mysql_password",
                     "projects.frontend_dir", "projects.backend_dir", "ai.api_key"]:
            v = dig(cfg, expr)
            if isinstance(v, str) and v.strip() and v.strip().lower() not in COMMON and len(v.strip()) >= 5:
                out.append(v.strip())
    return out

# 先确保没有旧实例占用 18765（否则新进程会被单实例机制直接退出，请求打到旧实例上）
subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"],
               capture_output=True)
time.sleep(2)

if os.path.isdir(TMP):
    shutil.rmtree(TMP)
os.makedirs(TMP)
shutil.copy2(EXE, os.path.join(TMP, "EnvKit.exe"))

exe = os.path.join(TMP, "EnvKit.exe")
p = subprocess.Popen([exe], cwd=TMP)
print("pid:", p.pid)
time.sleep(7)

fail = []
try:
    page = urllib.request.urlopen("http://127.0.0.1:18765/", timeout=10).read().decode("utf-8", "ignore")
    m = re.search(r"__EK_TOKEN__[^0-9a-fA-F]*([0-9a-fA-F]{16,})", page)
    tok = m.group(1)
    req = urllib.request.Request("http://127.0.0.1:18765/api/config",
                                 headers={"X-EnvKit-Token": tok})
    raw = urllib.request.urlopen(req, timeout=10).read().decode("utf-8", "ignore")
    j = json.loads(raw)

    checks = [
        ("chain.ssh_host", j["chain"]["ssh_host"], ""),
        ("chain.ssh_user", j["chain"]["ssh_user"], ""),
        ("chain.ssh_password", j["chain"]["ssh_password"], ""),
        ("chain.ssh_host_key", j["chain"]["ssh_host_key"], ""),
        ("chain.chain_dir", j["chain"]["chain_dir"], ""),
        ("projects.frontend_dir", j["projects"]["frontend_dir"], ""),
        ("projects.sql_file", j["projects"]["sql_file"], ""),
        ("ai.api_key", j["ai"]["api_key"], ""),
    ]
    print("--- 内嵌默认值 ---")
    for name, got, want in checks:
        ok = (got == want)
        if not ok:
            fail.append(name)
        print(f"{'OK ' if ok else 'BAD'} {name:<24} = [{got}]")

    print("chain_guard  =", j["chain"]["chain_guard"], "(应 False，否则会去连别人的服务器)")
    if j["chain"]["chain_guard"]:
        fail.append("chain_guard")
    print("ai.enabled   =", j["ai"]["enabled"])
    print("components   =", [c["name"] for c in j["components"]])
    if len(j["components"]) != 3:
        fail.append("components")

    print("--- 全文敏感串 ---")
    for pat in secrets_from_config():
        hit = pat in raw
        if hit:
            fail.append(pat)
        shown = pat if len(pat) <= 18 else pat[:15] + "..."
        print(f"{'BAD' if hit else 'OK '} {shown:<18} present={hit}")
except Exception as e:
    print("ERROR:", e)
    fail.append("exception")
finally:
    p.kill()
    time.sleep(1)
    subprocess.run(["taskkill", "/F", "/IM", "EnvKit.exe"], capture_output=True)
    print("--- 临时目录残留 ---", os.listdir(TMP))

print("\nRESULT:", "PASS" if not fail else "FAIL " + str(fail))
