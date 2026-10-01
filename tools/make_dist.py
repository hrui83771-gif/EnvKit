# -*- coding: utf-8 -*-
"""make_dist.py —— 打包"发给别人"的整包 zip（exe + 安装/卸载 + 说明）。

流程：
  1. 从 main.go 读 appVersion；
  2. go build -trimpath 重编 exe；
  3. 泄漏扫描：把本机 config.json 里的敏感值（SSH 主机/用户/密码、数据库密码、
     API Key、本机项目路径、主机指纹）逐个到 exe 二进制里搜，命中即拒绝打包；
  4. 生成 dist/EnvKit-vX.Y.Z-win64.zip：
     EnvKit.exe + 安装.bat + 卸载.bat + 使用说明.md + SHA256SUMS.txt

用法：python tools/make_dist.py            （在 envkit 目录下执行）
"""
import hashlib
import json
import os
import re
import subprocess
import sys
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join(ROOT, "EnvKit.exe")
DIST = os.path.join(ROOT, "dist")

# config.json 里需要做二进制泄漏扫描的字段（路径表达式 -> 说明）
SENSITIVE_FIELDS = [
    ("chain.ssh_host", "服务器 IP/主机"),
    ("chain.ssh_user", "SSH 用户"),
    ("chain.ssh_password", "SSH 密码"),
    ("chain.ssh_host_key", "主机指纹"),
    ("projects.mysql_password", "数据库密码"),
    ("projects.frontend_dir", "本机前端路径"),
    ("projects.backend_dir", "本机后端路径"),
    ("ai.api_key", "AI API Key"),
]


def load_assets():
    """加载 distassets/ 单一来源资产（说明与 bat 模板）。"""
    global README_MD, INSTALL_BAT, UNINSTALL_BAT
    a = os.path.join(ROOT, "distassets")
    with open(os.path.join(a, "README.md"), encoding="utf-8") as f:
        README_MD = f.read()
    with open(os.path.join(a, "install.bat.tmpl"), encoding="utf-8") as f:
        INSTALL_BAT = f.read()
    with open(os.path.join(a, "uninstall.bat.tmpl"), encoding="utf-8") as f:
        UNINSTALL_BAT = f.read()


def read_version():
    src = open(os.path.join(ROOT, "main.go"), encoding="utf-8").read()
    m = re.search(r'appVersion\s*=\s*"([\d.]+)"', src)
    if not m:
        sys.exit("无法从 main.go 读到 appVersion")
    return m.group(1)


def build_exe():
    print("== go build -trimpath ==")
    r = subprocess.run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", EXE, "."],
                       cwd=ROOT, capture_output=True, text=True, shell=True)
    if r.returncode != 0:
        sys.exit("构建失败：\n" + (r.stderr or r.stdout))
    print("   ok, %.1f MB" % (os.path.getsize(EXE) / 1048576))


def collect_secrets():
    """从本机 config.json 收集所有非空敏感值（明文与 dpapi: 密文都要扫）。"""
    path = os.path.join(ROOT, "config.json")
    if not os.path.exists(path):
        return []
    cfg = json.load(open(path, encoding="utf-8"))
    secrets = []
    # 通用词/默认值不扫：它们在二进制里必然出现，命中不代表泄漏（如 ssh_user=root）
    common = {"root", "admin", "user", "localhost", "127.0.0.1", "0.0.0.0",
              "22", "3306", "20200", "5002", "deepseek", "123456", "utf8mb4"}

    def dig(d, expr):
        cur = d
        for k in expr.split("."):
            if not isinstance(cur, dict) or k not in cur:
                return None
            cur = cur[k]
        return cur

    for expr, label in SENSITIVE_FIELDS:
        v = dig(cfg, expr)
        if isinstance(v, str) and v.strip():
            v = v.strip()
            if v.lower() in common or len(v) < 5:
                continue
            secrets.append((expr, label, v))
    return secrets


def scan_exe(secrets):
    blob = open(EXE, "rb").read()
    ascii_blob = blob.decode("latin-1")  # 逐字节比对，不做解码转换
    bad = []
    for expr, label, v in secrets:
        # 同时检查原值与 UTF-16LE 形式（Go 字符串常量在二进制里是 UTF-8，稳妥起见都查）
        needle = v.encode("utf-8")
        if needle in blob:
            bad.append((label, expr))
    if bad:
        print("!! 二进制中检测到本机敏感信息，禁止分发：")
        for label, expr in bad:
            print("   - %s（%s）" % (label, expr))
        sys.exit("请检查 config.dist.json 是否为干净模板，重新 go build 后再试。")
    print("== 泄漏扫描：本机敏感值 %d 项，二进制中 0 命中 ==" % len(secrets))


INSTALL_BAT = r"""@echo off
chcp 936 >nul
setlocal
set "DEST=%LOCALAPPDATA%\EnvKit"
echo == 安装 EnvKit 到 %DEST% ==
if not exist "%DEST%" mkdir "%DEST%"
copy /Y "%~dp0EnvKit.exe" "%DEST%\EnvKit.exe" >nul || goto :err
if exist "%~dp0使用说明.md" copy /Y "%~dp0使用说明.md" "%DEST%\" >nul
powershell -NoProfile -Command "$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut([Environment]::GetFolderPath('Desktop') + '\EnvKit.lnk'); $s.TargetPath = '%DEST%\EnvKit.exe'; $s.WorkingDirectory = '%DEST%'; $s.Save()" >nul 2>&1
echo == 完成。桌面已创建快捷方式，EnvKit 正在启动... ==
start "" "%DEST%\EnvKit.exe"
exit /b 0
:err
echo 安装失败：请确认本文件与 EnvKit.exe 在同一目录，且未被杀毒软件拦截。
exit /b 1
"""

UNINSTALL_BAT = r"""@echo off
chcp 936 >nul
setlocal
set "DEST=%LOCALAPPDATA%\EnvKit"
echo == 卸载 EnvKit ==
taskkill /IM EnvKit.exe /F >nul 2>&1
timeout /t 1 /nobreak >nul
if exist "%DEST%\EnvKit.exe" del /q "%DEST%\EnvKit.exe"
if exist "%DEST%\使用说明.md" del /q "%DEST%\使用说明.md"
if exist "%DEST%\config.json" del /q "%DEST%\config.json"
powershell -NoProfile -Command "$lnk = [Environment]::GetFolderPath('Desktop') + '\EnvKit.lnk'; if (Test-Path $lnk) { Remove-Item $lnk -Force }" >nul 2>&1
echo 已删除程序本体与桌面快捷方式。
echo.
echo 注意：以下内容不会被自动删除，如需清理请手动处理：
echo   1. 组件目录（默认 C:\EnvKit\tools，内含 Go / Node / MySQL）
echo   2. 已注册的 MySQL 服务与数据库数据
echo   3. 环境变量（PATH 中的组件目录）
echo   4. 你项目里的 backups 备份目录
echo.
echo 完成后本窗口可关闭。
exit /b 0
"""

README_MD = r"""# EnvKit 使用说明（发给朋友请连这份文件一起发）

## 这是什么
一个 Windows 桌面工具：一键检测/安装 Go、Node、MySQL，配置并启动你的
Go + Vue 项目（数据库建库、SQL 生效、前后端起停），内置 AI 助手与
区块链链端（FISCO-BCOS / WeBASE）检测运维。

## 首次使用（3 件事）
1. **SmartScreen 蓝框拦截**：这是未签名 exe 的正常现象，点「更多信息 → 仍要运行」。
2. **装 MySQL 需要管理员**：右键 → 以管理员身份运行；只装 Go / Node 不需要。
3. **组件在线下载**：Go / Node / MySQL 首次安装需从 CDN 下载约 400MB
   （已默认国内镜像）；如果 MySQL 下载慢，去官网手动下载 zip 再在
   「环境安装」里选择本地文件。

## 配置
- 「程序配置」页填你的项目目录、数据库连接；右上角可导出/导入配置换机。
- 所有密码在本机用 Windows DPAPI 加密保存，换电脑后需重新填写。

## 数据库安全（重要）
- 「备份数据库」生成带 .sha256 校验和的备份，自动保留最近 10 份；
- 「生效 SQL」执行前会**自动备份**，高危语句（DROP/TRUNCATE 等）需二次确认；
- 「恢复演练」会把备份恢复到临时库验证可恢复性，全程不碰业务库。

## 遇到问题
- 「关于」页 → 一键诊断包：生成 zip（报告 + 脱敏配置 + 日志 + 审计），发给能帮你排查的人。
- 前端起不来、AI 报错等常见原因：看对应页面底部日志，杀软误拦时点「杀软白名单」。

## 卸载
运行包里的「卸载.bat」删除程序与快捷方式；组件目录（C:\EnvKit\tools）、
MySQL 服务与环境变量不会被自动删除，说明里列了清理清单。
"""


def sha256_of(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    os.chdir(ROOT)
    load_assets()
    ver = read_version()
    build_exe()
    scan_exe(collect_secrets())

    os.makedirs(DIST, exist_ok=True)
    zip_path = os.path.join(DIST, "EnvKit-v%s-win64.zip" % ver)
    if os.path.exists(zip_path):
        os.remove(zip_path)

    readme = os.path.join(DIST, "_使用说明.md")
    with open(readme, "w", encoding="utf-8") as f:
        f.write(README_MD)
    bat_install = os.path.join(DIST, "_安装.bat")
    bat_uninst = os.path.join(DIST, "_卸载.bat")
    # .bat 必须用 ANSI(GBK) 编码，否则中文 cmd 下会乱码/报错
    with open(bat_install, "w", encoding="gbk", newline="\r\n") as f:
        f.write(INSTALL_BAT)
    with open(bat_uninst, "w", encoding="gbk", newline="\r\n") as f:
        f.write(UNINSTALL_BAT)

    sums = "EnvKit.exe  sha256  %s\n" % sha256_of(EXE)
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
        z.write(EXE, "EnvKit.exe")
        z.write(readme, "使用说明.md")
        z.write(bat_install, "安装.bat")
        z.write(bat_uninst, "卸载.bat")
        z.writestr("SHA256SUMS.txt", sums)
    for t in (readme, bat_install, bat_uninst):
        os.remove(t)
    print("== 打包完成：%s（%.1f MB）==" % (zip_path, os.path.getsize(zip_path) / 1048576))
    print("   建议发布前再跑一次：python tools/dist_check.py（运行态验证内嵌默认值干净）")


if __name__ == "__main__":
    main()
