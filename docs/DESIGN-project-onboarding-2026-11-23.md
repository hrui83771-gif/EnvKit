# 项目接入向导设计（2026-11-23）

> 目标：用户只填前端/后端目录，EnvKit 自动识别项目类型、启动方式、端口与数据库配置，
> 并在同一个结果页完成“测试连接 → 建库 → 导入 SQL → 保存”。检查阶段保持只读。

## 1. 完整流程

```
选择前后端目录
  → POST /api/project/inspect（只读）
  → 12 步检查动画（真实结果驱动）
  → 结果页：类型 / 端口 / 工具链 / 数据库候选 / SQL 文件
  → 可选的 POST /api/project/db-test（只读连接测试）
  → POST /api/project/apply（保存目录与数据库配置）
  → 可选的 POST /api/program/init-db（建库 + 导入 SQL）
  → 回到主界面
```

检查与写入分开：`inspect` 不连接 MySQL、不建库、不执行 SQL；只有用户勾选并保存后，
才进入 `apply` 与 `init-db`。

## 2. 目录选择：本地与远程必须分开

### 问题

2026-11-23 的真实反馈：向导点“选择”后打开了 SSH 远程目录浏览器，用户被带进
`/root/...` 这类 Linux 路径，再拿它做本地 `os.Stat`，结果显示“本地目录无法识别”。

### 原因

`95-project-setup.js` 调用文件浏览器时把最后一个参数 `remote` 传成了 `true`：

```js
openBrowser('dir', input.value || '', '', 'dir', callback, true);
```

`openBrowser` 的语义是：

- `remote=false`：Windows 本地目录，走 `/api/ls`；
- `remote=true`：通过 SSH 浏览远程 Linux 目录，走 `/api/remote-ls`。

### 修复

- 向导只允许本地目录，改为 `remote=false`；
- 本地浏览器如果收到旧配置/手输的 `/root/...` 路径，自动退回“此电脑”，
  不再把“远程路径在本地不存在”误报成“本地目录不可用”；
- 记住最后一次本地目录（`ek-last-local-dir`），下次打开从上次位置继续；
- 远程目录浏览器仍保留给链端/SSH 场景，不在项目接入向导里复用。

## 3. JDK 选择：检测与启动使用同一口径

### 问题

真实日志（2026-11-23 21:44）：

```
[WARN] [后端] 进程已退出：exit status 1
[ERROR] [Help 1] .../MojoExecutionException
```

`pom.xml` 要求 Java 17；`JAVA_HOME` 指向 JDK 8，`PATH` 里没有 `java`，
Maven 用 JDK 8 编译 Java 17 字节码，运行时只报 `UnsupportedClassVersionError`。

### 修复

`program.go` 新增 `javaForProject(dir)`：

1. 先看当前 `PATH` / `JAVA_HOME` 的 java；
2. 如果版本低于 `pom.xml` 的 `java.version`，扫描常见安装根：
   `%ProgramFiles%\Java`、Eclipse Adoptium、Microsoft、Amazon Corretto、Zulu、
   BellSoft、`%LOCALAPPDATA%\Programs\Eclipse Adoptium`、`%USERPROFILE%\.jdks`、
   EnvKit `tools` 目录与配置里的 JDK 组件；
3. 选满足要求的最高版本；
4. 启动时只对子进程切换 `JAVA_HOME`，不改系统环境变量；
5. 找不到兼容 JDK 时在启动前直接返回可读错误，不再让 Maven 输出
   `MojoExecutionException` 让用户猜。

检查页使用同一函数。真实测试项目要求 Java 17，当前 `JAVA_HOME` 是 JDK 8，
检查结果自动显示：

```
JDK 23.0.2
启动时会使用兼容 JDK：C:\Users\...\.jdks\openjdk-23.0.2\bin\java.exe
```

不再误报“启动会失败”。

## 4. 数据库：候选、测试、建库、导入

### 4.1 候选扫描

覆盖：

- `application*.yml / yaml / properties`
- `.env*`
- `config*.yaml`
- `docker-compose*`
- Go `main.go / db.go / config.go`

约束：

- 单文件 ≤ 512 KB，最多约 60 个文件，目录深度 3；
- 跳过 `node_modules`、`.git`、`target`、`dist`、`build`、`vendor`；
- 只合并同一文件/同一配置段字段；
- 只有 `${DB_PASSWORD}` 这类占位符做精确同名解析；
- 响应只给 `has_password`、`password_source`、`unresolved`，绝不返回密码值。

### 4.2 SQL 文件扫描

`scanProjectSQLFiles` 扫描：

- 前端目录；
- 后端目录；
- 前后端同父时，额外扫描项目根（例如 `tcm-inventory/sql/schema.sql`）。

深度 3，最多 12 个，跳过依赖/构建目录。排序优先：

1. `schema.sql`
2. `init.sql` / `init_db.sql`
3. `database.sql` / `db.sql`
4. `data.sql`
5. 其他 `.sql`

### 4.3 测试连接

`POST /api/project/db-test`：

请求：

```json
{
  "frontend_dir": "D:\\project\\web",
  "backend_dir": "D:\\project\\server",
  "db_id": "e511d51d22"
}
```

后端重新扫描候选，按 ID 匹配，用候选的 host / port / user / password 调
`mysql -N -B -e "SELECT VERSION();"` 和 `information_schema.SCHEMATA` 查询。
返回：

```json
{
  "connected": true,
  "exists": true,
  "version": "8.0.40",
  "checked_at": "2026-11-23T22:06:56+08:00"
}
```

密码只进 `MYSQL_PWD` 环境变量，不进 URL、日志、响应。

### 4.4 建库与导入

保存后复用现有 `/api/program/init-db`：

- `CREATE DATABASE IF NOT EXISTS <库名> CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci`
- 可选 `applySQLFile(path, db)`
- 进度写入 `progState["init-db"]`，向导轮询 `/api/program/state`

前端只在前端用户明确勾选时执行：

- `保存后建库（库不存在时）`：默认勾选；
- `建库后导入 SQL`：默认不勾，需选择文件；
- 多候选时必须手动选一组；
- 密码未解析时候选仍可展示，但不默认建议写入。

### 4.5 安全边界

- `inspect` / `db-test` 只读；
- 建库只创建 database，不创建 MySQL 实例、用户、授权；
- SQL 导入沿用现有执行路径，未做“已有表自动备份”；
- 已有库导入可能覆盖/追加同名表，UI 用“建库后导入 SQL”明确表达，并保留程序配置页的备份/还原能力；
- 配置保存后如果建库/导入失败，界面提示“配置已保存，但数据库操作失败”，不假装整体成功。

## 5. API 与文件

| 接口 | 作用 |
|---|---|
| `POST /api/project/inspect` | 只读检查项目与数据库候选、SQL 文件 |
| `POST /api/project/db-test` | 只读测试候选数据库连接与库是否存在 |
| `POST /api/project/apply` | 保存前后端目录与数据库配置 |
| `POST /api/program/init-db` | 建库并可选导入 SQL（异步） |

主要文件：

- `projectsetup.go`：检查、候选解析、SQL 扫描、db-test
- `projectsetup_test.go`：不泄密、SQL 排序与跳过依赖目录
- `program.go`：JDK 选择、Maven 启动前置校验
- `web/src/js/95-project-setup.js`：三屏向导、数据库操作、进度轮询
- `web/src/js/20-shell.js`：本地目录浏览器与远程浏览器的使用边界
- `web/src/app.css`：向导与数据库操作区样式

## 6. 已验证

真实测试项目：`D:\BCGD\2027\tcm-inventory`（`web` + `server`）。

- 前端：Vite，`npm run dev`，端口 5173，来源 `vite.config.js`，置信度 85%
- 后端：Spring Boot / Maven，`mvn spring-boot:run`，端口 8080，来源 `application.yml`，置信度 85%
- 数据库：`tcm_inventory`，`localhost:3306`，用户 `root`，密码来自 `application-local.yml`
- db-test：`connected=true`，`exists=true`，MySQL `8.0.40`
- SQL 扫描：识别到 6 个文件，`sql/schema.sql` 排第一
- 建库 + 导入：用临时库 `envkit_wizard_probe` 执行建表和插入，`init-db` 返回成功
- 向导保存：实际点击“保存并进入主界面”，toast 显示“项目已接入，数据库已创建”

## 7. 已知边界

- 当前只覆盖 MySQL；
- 不自动创建 MySQL 服务/用户/授权；
- SQL 导入不强制先备份，已有库上的高风险 SQL 仍依赖程序配置页的确认/备份流程；
- 多项目档案、端口冲突一键改端口、试启动并打开首页尚未进入本向导；
- 页面在 774px 视口下仍有主内容横向溢出，属于既有布局问题，已记录为后续全局体验项。
