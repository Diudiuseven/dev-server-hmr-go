# dev-server-hmr-go

本地开发用的**增量构建 + 热更新（HMR）服务**。保存源码后，只有受影响的模块会被
重建，新结果通过 Server-Sent Events 推送给已连接的客户端，无需全量重建或手动刷新。

仅依赖 Go 标准库（无第三方依赖）。

## 快速开始

```bash
# 1. 编写配置（见 devserver.example.json）
# 2. 启动
go run ./cmd/devserver -config devserver.json
# 3. 浏览器/开发客户端连接
#    GET  http://localhost:8080/api/state   当前全部模块版本与状态
#    GET  http://localhost:8080/api/events  SSE 热更新流
#    GET  http://localhost:8080/artifacts/<module>/<path>  当前产物
```

## 构建与更新语义

### 1. 增量传播，只构建受影响模块

模块依赖图在配置中声明。修改模块 `X` 后，重建集合 = `{X} ∪ 所有传递依赖 X 的模块`，
并按**依赖先后**（拓扑序）构建；互不相关的模块不会被构建。同层无依赖关系的模块
最多并发 `max_parallel_builds` 个。

### 2. 变更合并（debounce），且不丢最后一次变更

- 编辑器在短时间内的大量重复保存通知，会在 `debounce_window` 安静窗口内合并为
  **一次**构建。
- 构建进行中到达的变更会被记录为“脏”集合：当前构建结束后立即再跑一次（同样先做
  防抖合并），直到脏集合为空。因此**变更不会丢失**，且不会产生多余构建。
- 最终产物与“从零全量构建”一致：初始启动即执行一次全量构建，后续增量收敛到同一结果。

### 3. 内容寻址版本号（相同内容不产生新版本）

每个模块的版本号是对以下内容做 SHA-256 得到：

- 模块名；
- 其所有依赖模块的版本号；
- 本次构建产物的内容（路径 + 字节）。

所以：

| 情况 | 版本号 |
| --- | --- |
| 源码未变，重复构建 | **不变**（`UpdatedAt` 也不变） |
| 源码或产物变化 | 变化 |
| 依赖模块的版本变化 | 变化（自动传播） |
| 构建失败 | **保持上一个成功版本**，状态变为 `error` |

客户端用版本号判断“是否需要更新”，用状态字段区分“内容未变”与“构建失败”。

### 4. 模块状态

| status | 含义 | version / artifacts |
| --- | --- | --- |
| `building` | 正在构建 | 保留上次值 |
| `ok` | 最近一次构建成功 | 当前版本与产物 |
| `error` | 最近一次构建失败 | **保留上一个成功版本与产物**，并带 `error` 信息 |

单个模块构建失败不影响其他模块：其它模块继续可用，失败模块继续对外提供上一个
成功产物，HTTP 响应头 `X-Module-Status: error`、`X-Module-Error: ...` 暴露失败。
修复后再次保存，模块自动恢复为 `ok`。

### 5. 原子替换

每次构建周期在锁内构造**全新的不可变快照**（所有模块状态 + 产物），随后用一次
指针替换对外发布。读者在任意时刻读到的要么是旧的完整快照，要么是新的完整快照，
绝不会读到写了一半的结果。旧快照指针永久保持旧版本，不会被就地修改。

### 6. 新客户端与断开的客户端

- 新的 SSE 客户端一连入，立即收到**当前完整快照**（不用等待下一次变更）。
- 慢客户端（缓冲写满）会被直接断开，而不是阻塞构建或拖慢其他客户端；重连后通过
  首帧重新拿到最新快照。
- 客户端主动断开只取消自己的流，不影响任何后续构建。

### 7. 超时 / 取消

每个构建周期有 `build_timeout` 上限；客户端断开请求只取消该 HTTP 请求，不影响
构建状态。超时/取消时，尚未完成的模块记为明确的 `error`，周期仍然原子提交，
服务不会停留在 `building` 中间态。

## HTTP 协议

### `GET /api/state`

```json
{
  "epoch": 12,
  "modules": [
    {"name": "lib", "version": "e39e...", "status": "ok",
     "updated_at": "2026-10-04T18:37:38Z"},
    {"name": "app", "version": "5813...", "status": "error",
     "error": "build of app failed: ...", "updated_at": "..."}
  ]
}
```

`epoch` 在每次提交（含 building 过渡帧）后单调递增，可用于判断消息新旧。

### `GET /api/events`（SSE）

连接即收到当前快照，随后每次原子提交推送一帧：

```
event: update
data: {"epoch":13,"modules":[...]}
```

客户端处理建议：比较每模块 `version` 决定是否拉新产物；`status == "error"` 时
展示错误但保留旧版本；`building` 为过渡态可忽略。

### `GET /artifacts/<module>/<path>`

返回该模块当前产物。响应头：

- `ETag`：`"<version>-<path>"`，配合 `If-None-Match` 得到 `304`（内容未变）；
- `X-Module-Version`、`X-Module-Status`、`X-Module-Error`。

## 配置项

配置为 JSON 文件（见 `devserver.example.json`）。

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `workspace` | string | `.` | 工作区根目录（会转为绝对路径） |
| `address` | string | `:8080` | HTTP 监听地址，可用 `-addr` 覆盖 |
| `debounce_window` | duration 字符串 | `150ms` | 变更合并的安静窗口 |
| `build_timeout` | duration 字符串 | `30s` | 单个构建周期超时 |
| `max_parallel_builds` | int | `4` | 同层无依赖模块的最大并发构建数 |
| `watch_poll_interval` | duration 字符串 | `200ms` | 文件系统轮询间隔 |
| `watch_extra` | string[] | `[]` | 模块目录之外额外监听的目录 |
| `modules` | 数组 | 必填 | 模块定义 |

模块字段：

| 字段 | 说明 |
| --- | --- |
| `name` | 唯一模块名（必填） |
| `dir` | 源码目录，相对 `workspace`（默认取 `name`） |
| `deps` | 依赖的模块名（不可成环、不可引用不存在的模块） |
| `command` | 可选构建命令（在模块目录下执行），如 `["sh","-c","npm run build"]`；执行时注入环境变量 `DEP_VERSION_<模块名>` |
| `artifact_dir` | 配置 `command` 时，从该目录收集产物（相对模块目录） |
| `include` | 不配置 `command` 时，按 glob 收集源文件作为产物，默认 `**/*`；支持 `**` 递归，默认忽略 `.git`、`node_modules` |

命令行参数：

- `-config`：配置文件路径（默认 `devserver.json`）
- `-addr`：覆盖监听地址

监听范围 = 所有模块的 `dir` + `watch_extra`（已存在的目录）。编辑器临时文件
（`*~`、`*.swp`、`*.tmp`、`.#*`、`#*#`）被忽略，原子保存产生的最终重命名仍会被捕获。

## 目录结构

```
cmd/devserver   入口：配置、信号驱动的优雅关闭
internal/config    配置加载与校验（JSON、duration、默认值）
internal/graph     依赖图：拓扑排序、影响面传播
internal/version   内容寻址版本号（SHA-256）
internal/artifact  不可变快照与产物类型
internal/store     原子快照存储、失败保留上一成功版本
internal/compiler  构建执行（外部命令 或 include glob），可注入
internal/builder   增量编排：防抖、脏集合、并发、超时、失败隔离
internal/watcher   无依赖的轮询文件监听
internal/hub       SSE 客户端注册与非阻塞广播
internal/server    HTTP/SSE 服务
```

## 测试

```bash
go test -race ./...
```

覆盖：依赖传播与无关模块不重建、变更突发合并、构建中变更不丢、内容重复不产生
新版本、构建失败隔离与自动恢复、超时不留中间态、并发推送、慢/断开客户端不阻塞、
原子替换无撕裂状态，以及真实文件系统 + HTTP/SSE 的端到端用例。
