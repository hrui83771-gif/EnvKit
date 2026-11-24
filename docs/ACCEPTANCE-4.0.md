# EnvKit 4.0 验收（Phase 6）

> 本文件记录 4.0 契约能力的可复现实测。当前只覆盖 readonly 主干、布局与门禁；`enforce` 的真实写操作、事务回滚和人工界面验收仍待补，4.0 还不能标记为可发布。

## 结论

- **readonly 主干通过**：契约读取、Doctor 诊断、Apply 计划都只读；readonly 下执行 Apply 被 409 拦下，没有生成快照，也没有启动服务。
- **enforce 入口通过**：切 enforce 未带确认返回 428，模式保持 readonly。
- **按需扩展边界通过**：额外本地服务只显示“未启用扩展”，不进入 Doctor drift，也不进入 Apply。
- **设置页布局通过**：契约 Doctor、环境事务与快照、MCP 收进默认折叠的「高级」；基础区 13 张卡自动配平，无空洞。
- **未通过/未执行**：enforce 下的真实启动、执行前快照、失败停止、回滚，以及人工界面验收。发布前必须在隔离项目补跑。

## 基线

- 仓库：`D:\BCGD\FarmTrace\envkit`
- 分支：`feat/4.0-contract`
- 提交：`519018a feat(4.0): 设置页高级折叠与紧凑多栏布局`
- 运行：`EnvKit.exe`，`http://127.0.0.1:18765`（API 需 `X-EnvKit-Token`）
- 真实契约：`D:\BCGD\2027\tcm-inventory\envkit.yaml`
- 契约模式：`readonly`
- 实测时间：2026-11-25 00:12（UTC+8）
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
| A9 | enforce 事务执行 | 执行前快照；失败停止；可回滚 | 未执行 | **待补** |
| A10 | 人工界面验收 | 真桌面/浏览器确认折叠、按钮和文案 | 仅 headless CDP，未人工确认 | **待补** |

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

### 门禁

- `go test ./...`：通过，102.124s
- `go vet ./...`：通过
- `python tools/web_build.py --check`：通过

## 发布前必须补做的 A9

A9 不能在当前真实项目上直接做，先复制一个隔离项目或使用测试库：

1. 显式确认切 `enforce`，确认后模式为 enforce。
2. 选中 `apply.services.web.start` / `apply.services.backend.start`，执行前确认生成快照；记录快照 ID、是否含数据库。
3. 人为制造一个动作失败，确认后续动作被标记 `skipped`，没有继续执行。
4. 在隔离库上执行一次“建库/导入 schema”，确认：
   - 执行前已备份；
   - 结果里有“回滚不会删除新建库”的提示；
   - 回滚需要再次确认，不自动覆盖。
5. 恢复一次快照，确认配置、数据库和服务现场可恢复，且项目里的 `envkit.yaml` 不被静默覆盖。

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
- A9 在隔离项目通过，并有快照/回滚记录；
- A10 人工界面验收通过；
- 4.0 设计说明、发布说明、README 与版本号完成；
- tag 构建产物与版本号一致，本地构建不直接上传发行包。
