<div align="center">

<img src="app.ico" width="72" alt="EnvKit" />

# EnvKit

在 Windows 上装开发环境、配项目、跑链端、启动前后端，并确认它真的跑起来了。

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Windows-blue?logo=windows)](https://github.com/hrui83771-gif/EnvKit)
[![Release](https://img.shields.io/github/v/release/hrui83771-gif/EnvKit)](https://github.com/hrui83771-gif/EnvKit/releases)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)

</div>

---

EnvKit 是一个单文件 Windows 程序，双击后浏览器会打开本地控制台，按四步走完：

1. **环境安装**：检测 Go / Node.js / MySQL，缺什么装什么
2. **程序配置**：项目目录、数据库、启动命令、备份与恢复
3. **链端校验**：SSH、FISCO-BCOS 节点、WeBASE-Front
4. **程序启动**：前后端启动、端口占用、实时日志

另外还有 AI 助手（看日志、查状态、执行受控操作）和操作审计（谁、何时、做了什么、结果如何）。

## 为什么要多一步验证

很多"一键启动"工具到命令返回就结束了：退出码 0，界面打一个绿色的「已启动」。

但退出码 0 只说明命令发出去了。端口可能没监听、进程可能起来又崩了、HTTP 可能返回 503、备份文件可能缺了触发器。EnvKit 会再用探针确认一遍：

- **服务**：进程在不在 → 端口有没有人听 → 监听者是不是这个进程 → HTTP 是否正常 → 观察窗内有没有崩
- **备份**：sha256 对不对 → 表 / 触发器 / 视图 / 存储过程是否齐全。文件完整不等于能还原，真实还原请用「恢复演练」
- **链端**：端口 + `fisco-bcos` 进程数 + 共识视图是否推进。空闲链块高不动是正常的，不能拿块高当判据

验证不过就显示「已执行，未验证」，而不是「已完成」。模型可以自由决定做什么，但"是否完成"由这些客观结果决定，不由它自己的描述决定。

## 界面

![主界面](docs/screenshot-home.png)

## 主要功能

**环境安装**
- Go / Node.js / MySQL 检测与安装，下载走镜像加速，带 SHA256 校验和失败重试
- 演练安装：只检查、不落盘
- GOPROXY、npm registry、下载代理可配

**程序配置**
- 项目目录、MySQL 连接、启动方式（自动推断或手填）
- 建库建表、SQL 执行、备份 / 还原 / 恢复演练
- 配置导入导出（可脱敏）

**链端运维**
- SSH 密码或私钥登录，页面内嵌终端
- 节点端口、进程数、共识状态检测
- 宕机自动恢复、后台守护、Windows 通知

**程序启动**
- 前后端启动 / 停止 / 端口诊断
- 子进程绑定 Job Object，EnvKit 退出时自动回收
- 前端 / 后端实时日志

**AI 助手**
- 26 个受控工具，写操作一律先确认
- 复验结果以三态徽章展示（通过 / 已执行未验证 / 失败），不采信模型自述
- 记忆与经验默认关闭；开启后可以查看每轮到底注入了哪些规矩
- 对话里的 Markdown、表格、代码块正常渲染

**操作审计**
- 主体、动作、目标、结果、耗时、复验结论、trace_id
- 按天分文件，保留 30 天

**托盘**
- 右键打开控制台 / 程序配置 / 日志目录
- 隐藏窗口到托盘，退出前自动停掉前后端

## 快速开始

### 下载

从 [Releases](https://github.com/hrui83771-gif/EnvKit/releases) 下载最新版本，解压后运行 `EnvKit.exe` 或 `安装.bat`。控制台默认在 `http://127.0.0.1:18765`，端口冲突时自动避让。

### 从源码构建

```bash
git clone https://github.com/hrui83771-gif/EnvKit.git
cd EnvKit
go build -trimpath -ldflags "-s -w" -o EnvKit.exe .
```

需要 Go 1.26 或更高（`go.mod` 里的版本要求）。图标与版本信息已通过 `resource.syso` 编进 exe。

### 运行前提

- Windows 10 / 11 x64
- MySQL 绿色版需要 [VC++ 2019 Redistributable (x64)](https://learn.microsoft.com/zh-CN/cpp/windows/latest-supported-vc-redist)
- 注册或启动 MySQL 服务需要管理员权限
- 首次安装组件需要联网（约 180 MB，可配下载代理）

## 配置

把 `config.json` 放在 exe 同目录即可覆盖内置默认值，涵盖组件版本与下载地址、GOPROXY、npm registry、MySQL 参数、项目目录、链端 SSH、托盘状态等。仓库里的 `config.dist.json` 是干净模板。

API Key 与数据库密码经 Windows DPAPI 加密，只在本机可解密。

## 安全边界

- 所有写操作先过权限裁决：`auto` / `confirm` / `elevated` / `forbidden`，禁止档在派发前终止
- 需确认的动作由服务端返回 428 强制确认，模型跳不过去
- 只暴露受控工具，不提供任意命令执行
- 文件探索限制在已配置的项目目录内
- 记忆只影响建议，不改变任何操作的权限档位
- SSH 首次连接按 TOFU 记录主机指纹，之后变化即拒绝

## 评测

仓库里有单元测试、浏览器回归，以及 11 类故障注入评测（每类 3 次真实调用）。评测抓到过三个真实缺陷：备份校验"假绿"、大文件截断误报、评测装置在测旧构建。

方法和原始数据在 [`docs/eval/`](docs/eval/)；README 不再贴一堆会过期的数字。

## 项目结构

Go 代码在模块根（`package main`），Windows 平台相关代码在 `internal/platform`。前端源码在 `web/src/`，构建时拼成单文件 `web/index.html`，再用 `go:embed` 打进 exe。

改前端只改 `web/src/`，然后 `python tools/web_build.py` 重拼；直接改 `web/index.html` 会在下次构建时被覆盖。

逐文件职责见 [`docs/CODEMAP.md`](docs/CODEMAP.md)，设计取舍见 [`docs/architecture.md`](docs/architecture.md)。

## 版本

当前 **3.2.0**。这一版的改动见 [`docs/RELEASE-v3.2.0.md`](docs/RELEASE-v3.2.0.md)，历史路线见 [`docs/ROADMAP.md`](docs/ROADMAP.md)。

## 许可证

[MIT](LICENSE)。

桌宠素材来自 [dafeiyu-pet](https://github.com/1190fasheqi/dafeiyu-pet)（MIT），托盘库 systray（Apache-2.0），前端内嵌 marked 与 highlight.js（MIT / BSD-3-Clause）。完整许可见 [THIRD-PARTY-NOTICE.md](THIRD-PARTY-NOTICE.md)。

## 免责声明

本软件按「现状」提供，作者不对准确性、可靠性、适用性作担保，使用后果由使用者自行承担。依赖的第三方组件（Go、Node.js、MySQL、FISCO-BCOS、WeBASE 等）版权归各自权利方所有。链上数据仅供参考，不构成投资或商业建议。完整声明见软件内「关于 / 免责声明」。
