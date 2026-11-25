# EnvKit 4.0 验收（Phase 6）

> 本文件记录 4.0 契约能力的可复现实测。readonly 主干、布局、`enforce` 数据库事务和服务失败停止已在隔离环境跑通；A10 按用户确认默认完成，未再做人工点击验收。

## 结论

- **readonly 主干通过**：契约读取、Doctor 诊断、Apply 计划都只读；readonly 下执行 Apply 被 409 拦下，没有生成快照，也没有启动服务。
- **enforce 入口通过**：切 enforce 未带确认返回 428，模式保持 readonly。
- **enforce 数据库事务通过**：隔离实例上完成“建库 → 导入 schema → 快照 → 回滚”。回滚前必须再次确认；快照包含数据库；恢复后新增表被真正删除，Doctor 明确显示缺少该表。
- **enforce 失败停止通过**：隔离实例上真实启动 web/backend，web 启动脚本以退出码 2 失败后事务立即停止，backend 未执行并标记“前序动作失败，未执行”；失败事务仍留有执行前快照，恢复入口需二次确认。
- **发现并修复真实缺陷**：空库快照恢复时，直接导入空 dump 不会删除当前库新增表；修复为恢复前先清空视图/表、始终校验表数，失败时用恢复前备份回填。
- **按需扩展边界通过**：额外本地服务只显示“未启用扩展”，不进入 Doctor drift，也不进入 Apply。
- **设置页布局通过**：契约 Doctor、环境事务与快照、MCP 收进默认折叠的「高级」；基础区 13 张卡自动配平，无空洞。
- **未通过/未执行**：A10 人工界面未由人逐项点击；按用户 2026-11-25 的确认，默认视为完成，依据是 40 项 headless CDP 布局/行为回归全部通过。

## 基线

- 仓库：`D:\BCGD\FarmTrace\envkit`
- 分支：`feat/4.0-contract`
- 提交：`b1737be fix(4.0): 恢复空库快照前清空库对象并始终校验表数`
- 运行：`EnvKit.exe`，`http://127.0.0.1:18765`（API 需 `X-EnvKit-Token`）；主实例已用修复版二进制重启，重启后契约仍 readonly、Doctor `ok=8 drift=2 undeclared=2 unknown=2`、计划 2 个 safe 动作、快照仍为 1
- 真实契约：`D:\BCGD\2027\tcm-inventory\envkit.yaml`
- 契约模式：`readonly`
- 实测时间：2026-11-25 00:37（UTC+8）
- 隔离实测：独立实例 `127.0.0.1:18805`（数据库事务）与 `127.0.0.1:18806`（失败停止），各自独立项目与 backups/checkpoints；测试完成后库、实例和临时目录均已清理
- 环境：Windows，系统 MySQL 8.0.40，JDK 17；Go/Node 由 EnvKit 组件目录提供

## 验收矩阵

| ID | 用例 | 期望 | 实测 | 结论 |
|---|---|---|---|---|
| A1 | 读取契约 | `exists=true`、`valid=true`，模式 readonly | 通过；无 problems | 通过 |
| A2 | Doctor 只读诊断 | 不启动、不修复；输出 drift/unknown/undeclared | `ok=8 drift=2 unknown=2 undeclared=2` | 通过 |
| A3 | Apply 计划 | 只生成白名单动作，不执行 | `readonly`，2 个 safe 动作：启动 web、启动 backend | 通过 |
| A4 | readonly 执行 Apply | 409 拦下，不生成快照、不改环境 | HTTP 409；快照数 1 → 1 | 通过 |
| A5 | 切 enforce 未确认 | 428 二次确认，模式不变 | HTTP 428；之后仍为 readonly | 通过 |
| A6 | 非核心本地服务 | 只显示“未启用扩展”，不算 drift | `TestDoctorExtraLocalServiceStaysExtensionDisabled` 通过 | 通过 |
| A7 | 工具链版本判断 | `>=`/范围满足即通过；显式 `=`/`==` 才精确比较 | `tools.java ok 17.0.11`；`TestExactVersionPinSyntax` 通过 | 通过 |
| A8 | 设置页高级折叠 | 契约/事务/快照/MCP 默认收起；基础区无洞 | 高级区 3 张卡、默认 closed、折叠高 35px；基础区 13 卡、两栏高差 115px | 通过（自动布局） |
| A9 | enforce 数据库事务 | 执行前快照；高风险确认；回滚真正还原数据库 | 隔离库完成建库→导入→快照→回滚；恢复需确认；新增表被删除，Doctor 显示 `database.tables=drift 缺少 a9_probe` | 通过 |
| A9b | enforce 服务启动与失败停止 | 执行前快照；首个失败后停止；后续 `skipped` | 隔离实例真实执行 web/backend 启动；web 退出码 2 失败，backend 标记“前序动作失败，未执行”；快照 `cp-20261125-003438-4a6cda` | 通过 |
| A10 | 人工界面验收 | 真桌面/浏览器确认折叠、按钮和文案 | 按用户确认默认完成；自动布局/行为回归 40 项全部通过，未逐项人工点击 | 通过（默认） |

## 关键实测

### Doctor

```
status=drift
ok=8  drift=2  unknown=2  undeclared=2  skip=0
```

未通过项只有服务端口未监听：

- `services.web.probe`：`http://127.0.0.1:5173/` 未监听
- `services.backend.probe`：`http://127.0.0.1:8080/actuator/health` 未监听

远程链端仍按设计保持 unknown：

- `services.chain`
- `services.webase`

数据库 schema/表清单未声明，输出 undeclared，不推断。

### Apply 计划

```json
{
  "mode": "readonly",
  "actions": [
    {"id": "apply.services.web.start", "risk": "safe", "available": true},
    {"id": "apply.services.backend.start", "risk": "safe", "available": true}
  ]
}
```

本轮没有建库、没有导入 schema，也没有启动 web/backend。

### readonly 拦截

POST `/api/contract/apply`，选中 web/backend 两个动作：

```json
{
  "http": 409,
  "body": {
    "ok": false,
    "error": "当前是 readonly 模式，只能生成计划；切到 enforce 才允许执行写操作"
  }
}
```

执行前后快照数量都是 1，说明拦截发生在任何写操作之前。

### enforce 确认

POST `/api/contract/mode`，`{"mode":"enforce","confirm":false}`：

```json
{
  "http": 428,
  "needConfirm": true,
  "action": "contract_mode",
  "target": "enforce",
  "reason": "切换到 enforce 后，环境事务可以执行建库、启动服务和导入 schema。高风险动作仍需逐次确认，执行前会自动快照。"
}
```

之后读取契约，模式仍是 `readonly`。

### 设置页

`node tools/cdp_settings_layout_test.js` 全部通过，关键数字：

- 基础区 13 张卡，高级区 3 张卡（契约 Doctor / 事务与快照 / MCP）
- 高级区默认 `open=false`，折叠高 35px
- 1400px 宽时两栏，卡片最小宽 406px
- 两栏高差 115px，同栏内无空洞
- 基础区整页 1727px
- 没有卡片横向溢出

主实例切换修复版二进制后又重跑一次，40 项全部通过。

### 门禁

- `go test ./...`：通过，105.480s
- `go vet ./...`：通过
- `python tools/web_build.py --check`：通过

## A9 隔离实测

为不影响真实 `tcm-inventory`，在 `tools\_a9` 复制 EnvKit.exe 与主配置，使用独立端口 18805、独立项目目录、独立 backups/checkpoints，并设置 `ENVKIT_INSTANCE_TAG=a9`。隔离项目只声明 MySQL 数据库和 schema，不声明 web/backend，避免触发真实服务启动。

### 首轮：暴露快照恢复缺陷

首轮库 `envkit_a9_20261125_001`：

1. 初始 Doctor 判定库不存在，计划只有 `apply.database.create`。
2. enforce 下建库成功，快照 `cp-20261125-001800-b66463`，`database_included=false`；回滚提示明确写明“建库动作不在快照回滚范围内；回滚不会删除本次新建的库”。
3. 建库后计划出现 `apply.database.schema`，risk 为 `confirm`；未确认执行返回 428，确认后导入成功，快照 `cp-20261125-001822-a231e0`，`database_included=true`，探针数据存在。
4. 恢复该快照未确认返回 428；确认后接口返回 `ok=true`，但 Doctor 仍显示 `database.tables=ok`，已导入的 `a9_probe` 表没有被删除。

根因是快照拍在建表前，数据库 dump 是空库 dump，只有头部、没有 `CREATE TABLE`；恢复时直接把空 dump 导入现有库，不会删除当前已有表。另一个放大了问题：恢复校验只在快照表数大于 0 时才比较，空库快照完全跳过校验，于是“恢复成功”与事实不符。

### 修复

`checkpoint.go` 的恢复路径改为：

- 新增 `dropDatabaseObjects`，恢复前查询 `information_schema.TABLES`，先删视图、再删表，标识符经 `quoteIdent` 转义；
- 新增纯函数 `dropObjectsSQL` 生成 `DROP VIEW/TABLE IF EXISTS`，拒绝非法标识符；
- 导入快照前先清空当前库对象，使 dump 导入等价于“替换整个库内容”；
- 表数量始终比较，包括快照表数为 0 的情况；
- 导入失败或表数不一致时，用恢复前刚生成的备份回填，并在消息中标记“已回滚到恢复前备份”；
- 新增 `TestDropObjectsSQLQuotesAndRejectsBadName`，覆盖空列表、多表转义和非法名拒绝。

提交：`b1737be`。

### 二轮：修复后通过

二轮库 `envkit_a9_20261125_002`：

1. 初始 Doctor 为 `drift`，计划只有 `apply.database.create`。
2. enforce 建库成功，快照 `cp-20261125-002233-36972f`，`database_included=false`；回滚提示仍明确不删除新建库。
3. 建库后计划只有 `apply.database.schema`，risk `confirm`；未确认返回 428。
4. 确认导入成功，快照 `cp-20261125-002234-2ad622`，`database_included=true`；查询得到 `["1","before-restore"]`。
5. 恢复快照未确认返回 428；确认后 `restore.database`、`restore.config` 均 OK，配置轮转为 `.bak`。
6. 恢复后直接查询 `a9_probe` 报 `Table ... doesn't exist`；Doctor 变为 `ok=7 drift=1 undeclared=1`，`database.tables` 显示“缺少 a9_probe”，证明新增表已被真正删除。
7. 备份现场：快照 dump 1362 字节、恢复前备份 2068 字节，两份 `.sql` 与 `.sha256` 均存在。

测试完成后已删除 `envkit_a9_20261125_001`、`envkit_a9_20261125_002`，停止 18805 隔离实例并移除 `tools\_a9`；主实例仍为 readonly，契约根仍指向 `D:\BCGD\2027\tcm-inventory`。

## A9b 服务失败停止实测

使用第二个隔离实例 `127.0.0.1:18806`，项目声明 web、backend 两个 local 服务，探针端口分别为 19001、19002，均未监听。web 的 `package.json` 里 `dev` 脚本故意以退出码 2 结束，用于制造真实启动失败。

1. 初始 Doctor `drift`，计划只有 `apply.services.web.start` 和 `apply.services.backend.start`，均为 safe 且 available。
2. 切 enforce 后提交两个动作，执行前自动生成快照 `cp-20261125-003438-4a6cda`，`database_included=false`；回滚提示为“配置和端口现场已快照…有动作失败，可回滚到执行前快照。”
3. web 真实执行 `npm run dev`，进程派生后立即退出，结果 `ok=false`，错误为“前端进程已退出，启动未成功”。
4. backend 没有被执行，结果在 `skipped` 中，文案为“前序动作失败，未执行”。
5. 事务 `ok=false`，但快照和回滚入口保留；恢复该快照未确认返回 428，确认后 `restore.config` 成功，配置恢复，当前配置轮转为 `.bak`。

这验证了真实进程失败下的停止语义：不是继续跑后端，也不是只报错不留现场。

## 发布前仍需留意

- 快照恢复范围已明确：只覆盖本机配置和可选数据库；服务/端口现场只记录、不自动启动或停止。若发布说明要承诺“还原服务运行现场”，需另开功能，不计入 4.0；
- A10 已按用户确认默认通过，但发布说明中应写明“未逐项人工点击”，避免把自动回归当成人工验收。

## 复跑命令

```powershell
# 1. 启动
Start-Process "D:\BCGD\FarmTrace\envkit\EnvKit.exe" -WorkingDirectory "D:\BCGD\FarmTrace\envkit"

# 2. 从首页提取 token 后调用
# GET  /api/contract
# POST /api/contract/doctor
# GET  /api/contract/apply/plan
# POST /api/contract/apply  {"selected":["apply.services.web.start","apply.services.backend.start"]}
# POST /api/contract/mode   {"mode":"enforce","confirm":false}

# 3. 前端布局
node tools/cdp_settings_layout_test.js

# 4. 全量门禁
C:\EnvKit\tools\go\bin\go.exe test ./...
C:\EnvKit\tools\go\bin\go.exe vet ./...
python tools/web_build.py --check
```

## 发布门槛

4.0 可发布必须同时满足：

- A1–A8 全部通过；
- A9 数据库事务在隔离项目通过，并有快照/回滚记录；
- A9b 服务启动与失败停止在隔离实例通过；
- A10 按用户确认默认完成；
- 4.0 设计说明、发布说明、README 与版本号完成；
- tag 构建产物与版本号一致，本地构建不直接上传发行包。
