# EnvKit v3.9.0

> 数据浏览从“只读”推进到“受限可写”：单格自动保存、自定义写 SQL、带口令的删库入口，并把这一块的界面重做了一遍。

## 发布信息

- 版本：`3.9.0`（`main.go` appVersion / `versioninfo.json` / `resource.syso` 三处一致，`tools/version_check.py` PASS）
- tag：`v3.9.0`（提交后按发版流程打在本提交；产物只从该 tag 构建一次）
- 基线：`feat/4.0-contract @ 2a00400` + 本次改动
- 产物：`EnvKit-v3.9.0-win64.zip`；SHA256 见包内 `SHA256SUMS.txt`，以及 Release 页的构建来源证明

## 数据浏览的受限写权限

### 单格修改

- 只在「表数据」页、且当前表有主键时开放。
- 主键列不可编辑；无主键表保持只读。
- 失焦即保存，不弹确认；服务端用全部主键列定位，执行 `UPDATE ... LIMIT 1`。
- 输入 `NULL` 会写成 SQL `NULL`；普通文本按字符串保存。

### 自定义写 SQL

- 默认仍走只读路径，SELECT / SHOW / DESC / EXPLAIN / WITH 不需要额外确认。
- 写语句必须先勾选「允许写操作」，再通过二次确认；服务端只放行单条 DML / 表级 DDL。
- `DROP DATABASE` / `DROP SCHEMA` 会被拒绝，必须走专用删库入口，避免自定义 SQL 绕过删库口令。

### 删除数据库

- 二次确认 + 输入口令 `123456`。
- 删除前自动 `mysqldump` 备份；备份失败则中止删除。
- 成功后返回备份路径、大小和 SHA256，并写入审计日志。

> 口令 `123456` 是弱口令，只用于本机防误触，不是安全边界。

### 本项目库定位

- `GET /api/db/list` 额外返回 `projects.db_name`。
- 库列表中本项目库排在最前并标记「本项目」；打开数据浏览时默认选中，另有「定位本项目库」按钮。

## 界面调整

- SQL 输入框改为主题边框、等宽字体和聚焦状态。
- 「允许写操作」从大块卡片改为紧凑胶囊复选框，勾选后使用主题色。
- 「运行」改为主动作按钮，和执行状态排成一行。
- SQL 页签自动隐藏单格编辑提示和分页器，只保留查询相关控件。
- 「删除数据库」放到工具栏右侧并使用危险操作样式。
- 侧栏加宽；本项目库名称过长时省略，悬停显示全名，「本项目」标记不再被挤掉。

## 验证

- `go build ./...`：通过
- `go test ./...`：通过
- `go test -run "TestDBWritableSQL|TestBuildCellUpdateSQL|TestDBOneStatement" .`：通过
- `python tools/version_check.py`：PASS（appVersion / versioninfo / syso 三处 3.9.0）
- `python tools/web_build.py --check`：PASS
- `python tools/docs_site.py --check`：PASS
- 隔离数据库实测：单格更新、复合主键定位、写 SQL 缺确认 428、错误口令 403、系统库 403、删库前备份、成功后删除均通过
- CDP 真实页面实测：本项目库标记、单格失焦保存、写 SQL 二次确认、删库二次确认与口令流程均通过

## 兼容性

- 配置 schema 未变，不需要迁移。
- 新增接口不影响旧页面；旧版页面不会调用写接口。
- 升级方式：替换 exe，或使用 v3.9.0 发行包覆盖安装。
