# EnvKit v3.8.1

> 发布策略修正版。用户可见功能不变；本版存在的理由是把"版本号 / tag / CI 构建 / 发行包"重新对齐，并修掉两处会静默出错的工程问题。

## 发布信息

- 版本：`3.8.1`（`main.go` appVersion / `versioninfo.json` / `resource.syso` 三处一致，`tools/version_check.py` PASS）
- tag：`v3.8.1`（**指向发布提交，产物只从该 tag 构建一次**）
- 基线：`main@44ebafb`
- 产物：`EnvKit-v3.8.1-win64.zip`；SHA256 见包内 `SHA256SUMS.txt`，以及 Release 页的构建来源证明（`gh attestation verify ... -R hrui83771-gif/EnvKit`）

## 为什么必须发这个版本

v3.8.0 在同一个版本号下发布过两个内容不同的包：

| 时间 | 内容 | tag 指向 | 来源证明 |
|---|---|---|---|
| 2026-11-07 | 首发包 | `v3.8.0` → `4319cac` | 覆盖首发包 |
| 2026-11-12 | 重新构建，含引导页新改动，SHA256 不同 | 仍指向 `4319cac` | **不覆盖** |

结果是：下载到的包与 tag 构建不是同一份东西，`gh attestation verify` 也无法证明后者。README 当时只能写"以下载页 SHA256 为准"——这不是可验证发布，只是免责声明。

从 3.8.1 起执行两条硬规则：

1. 一个版本号只对应一次 tag 构建；任何重构建都必须升版本号。
2. 发行包只允许来自 tag 触发的 `release.yml`；本地 `make_dist.py` 只用于验收，不上传。

**v3.8.0 的两批同名包作废，请勿继续分发。**

## 本版修复

### 1. 配置的并发与深拷贝（用户无感，但是真问题）

`Config` 是包级全局值，内部又有指针、切片与 map。此前多处"加锁拷贝"只复制了头部：

- `state.go`：新增 `cfgSnapshot()` / `cfgUpdate()`。读取一律拿深拷贝，写入在单锁内完成并返回独立快照。
- `configfile.go`：`cloneConfigDeep` 从只拷贝 `AI`/`Dev` 扩展为覆盖 `Lan`/`MCP`/`Update`/`Notify`、服务与组件切片、`ScanPorts`/`WebScripts`、`AI.Quirks`、`Component.ExtraEnv`、`Probe.Parts`（递归）、`Lan.Devices` 及全部 `*bool`/`*int`；`saveExternalConfig` 入口先深拷贝。
  - 这一步顺带修掉一个隐蔽问题：`normalizeConfig` 会就地改 `Components[i].URL`，浅拷贝下那是在**无锁改全局配置的底层数组**。
- `handlers.go`：导入配置时，默认值只补到导入副本（原来调用 `applyConfigDefaults()`，直接改正在运行的全局配置）；"空密码保留本机密码"的判断移到补默认值之前，否则 `123456` 会顶掉保留逻辑。
- `update_config.go`：读改写整段进 `cfgUpdate`，落盘用返回的深拷贝，不再把全局配置值拷贝交给 `saveExternalConfig`。

### 2. 组件检测假阳性

`detect.go`：`BinExe` 为空的组件不再把"安装目录存在"当成"组件已装"。
修复前，只要默认 `InstallDir` 在机器上存在（装过任意组件的机器都满足），检测结果会是"已安装 · 版本为空 · 位置=安装目录"——这正是本机测试偶发失败的原因。

### 3. 测试与门禁

- `TestMavenStartEnvSwitch`：8.3 短路径（用户名含撇号时 `t.TempDir()` 返回 `HEN'R'~1`）与 `EvalSymlinks` 长路径不再按字面量比较，改用规范化后的 `sameFileDir`。产品行为不变，测试不再"CI 绿、本机红"。
- 新增 `config_access_test.go`：深拷贝隔离、默认值不碰全局、导入保留空密码。
- 新增 `ops_goldpath_test.go`：NodeURL 改写（缩进/行尾注释/无端口形态/IPv6）、备份保留策略、校验和读取、Java home 与版本比较。
- `eval-gate.yml`：单测从 `go test` 改为 `go test -race`——配置文件是全局状态，漏锁只有 race detector 抓得到。

## 兼容性

- 配置 schema 未变，不需要迁移；已有 `config.json` 可直接用。
- 无用户可见行为变化：UI、工具清单、权限档位、手机端接口均未改动。
- 升级方式：替换 exe，或使用 v3.8.1 发行包覆盖安装。

## 验证

- `go build ./...`：通过
- `go vet ./...`：通过
- `go test -race -count=1 .`：通过（97s，主包全量）
- `go test -count=1 ./internal/platform`：通过（该包为 Windows API 胶水，race 版在部分 MinGW 工具链上启动即 `0xc0000139`，属工具链问题；其测试为确定性单线程调用）
- `python tools/version_check.py`：PASS（appVersion / versioninfo / syso 三处 3.8.1）
- `python tools/docs_index.py --check`：PASS（21 节 / 71 块）
- `python tools/web_build.py --check`：PASS（前端产物与源一致）
