# HMR — 本地增量构建与热更新服务

一个面向本地开发的**增量构建 + 热更新**服务。改动某个源码模块后，它只重建
受依赖关系影响的模块，把新版本**原子地**发布并通过 Server-Sent Events 推送给
已连接的客户端，无需全量重建，也无需手动刷新。

- 依赖图驱动的拓扑增量构建（互不相关的模块不会被重复构建）
- 编辑器保存风暴合并（可配置防抖窗口），且**最后一次变更绝不丢失**
- 内容寻址版本号：同一份源码/产物重复出现不产生新版本
- 单模块构建失败时保留上一个成功版本并暴露明确失败状态，修复后自动恢复
- 产物原子替换：客户端任意时刻读到的要么是旧的完整版本，要么是新的完整版本
- 新连接客户端立即收到当前完整状态；慢/断开客户端永不阻塞构建
- 监听范围、合并窗口、并发构建数、端口等均可配置；超时与取消不会留下中间状态

## 快速开始

```bash
cp hmr.example.json hmr.json      # 按你的工程修改
go run ./cmd/hmr -config hmr.json
```

服务默认监听 `:8080`。可用 `-addr :9090` 临时覆盖端口。

每个模块可以声明构建命令；不声明命令时，服务会根据模块源码与依赖版本生成一个
确定性的合成产物（便于在没有真实构建器时使用与测试）：

```json
{
  "name": "app",
  "root": "packages/app",
  "deps": ["ui"],
  "command": "sh",
  "args": ["-c", "npm run build"],
  "artifact": "dist/app.js"
}
```

构建命令在模块的 `root` 目录内运行。若声明了 `artifact`，构建成功后从该文件
读取产物；否则使用命令的标准输出作为产物。命令还会收到环境变量
`HMR_MODULE`（模块名）与 `HMR_ARTIFACT_PATH`（建议的产物临时路径）。

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查 |
| GET | `/status` | 所有模块的完整当前状态与版本 |
| GET | `/modules/{name}` | 单个模块状态 |
| GET | `/artifacts/{name}/{version}` | 下载**当前**版本产物（旧版本返回 409，杜绝读到半成品） |
| GET | `/events` | SSE 事件流；连接后第一条消息为当前完整快照 |
| POST | `/rebuild` | 请求一次全量重建（异步调度） |

状态对象示例：

```json
{
  "module": "app",
  "status": "failed",
  "building": false,
  "versionId": "3f2d…",
  "error": "build app failed: …",
  "failedAt": 1759600000000000000,
  "changedAt": 1759600000000000000
}
```

`status` 取值为 `unknown`（尚未构建）、`building`、`ok`、`failed`。

## 事件（SSE）

连接 `/events` 后，客户端先收到一条 `snapshot` 消息（当前完整状态），随后收到
构建生命周期事件：

```
event: (SSE 默认 message)
data: {"type":"build:start","module":"core","at":…}
data: {"type":"build:ok","module":"core","versionId":"…","at":…}
data: {"type":"build:failed","module":"core","error":"…","at":…}
data: {"type":"idle","at":…}
```

### 客户端更新语义

客户端用 `versionId` 判断是否需要更新：

- **内容未变**：源码内容与依赖版本都没变时，不会产生新的构建或版本号，
  `versionId` 保持不变 → 客户端无需更新。
- **构建失败**：`status` 变为 `failed`，`error` 给出原因，同时 `versionId`
  **仍指向上一个成功版本**。客户端据此区分「内容没变」与「构建失败」：前者没有
  新事件/新版本，后者有 `build:failed` 事件和明确的错误字段，但可继续使用旧版本。
- **修复恢复**：源码修复后会自动重建，成功后产生新的 `build:ok` 与新 `versionId`。
- **原子替换**：只有在产物完整写入并 `rename` 落盘、状态提交后，才会发布
  `build:ok`。`/artifacts/...` 只提供当前版本，因此客户端永远不会读到写了一半的
  结果。
- **新客户端**：连接 `/events` 立即得到当前完整快照；随后与其他客户端一样接收增量
  事件。
- **断开/慢客户端**：事件发送不阻塞构建；缓冲已满的慢客户端会被跳过，断开的
  客户端会被自动移除，不影响后续构建。

## 配置项

| 配置 | 说明 | 默认值 |
| --- | --- | --- |
| `workspace` | 工作区根目录，模块路径相对它 | 配置文件所在目录 |
| `artifactDir` | 产物存储目录（相对 workspace） | `.hmr/artifacts` |
| `modules[].name` | 唯一模块名 | 必填 |
| `modules[].root` | 模块源码目录（相对 workspace） | 必填 |
| `modules[].deps` | 依赖的其它模块名（决定构建顺序与传播范围） | `[]` |
| `modules[].command` / `args` | 构建命令及参数；为空则使用合成产物 | 空 |
| `modules[].artifact` | 构建产物文件（相对 root）；为空用 stdout | 空 |
| `watch.roots` | 监听目录范围（相对 workspace） | 所有模块 root |
| `watch.include` | glob 白名单（相对路径或文件名） | 全部 |
| `watch.exclude` | glob 黑名单（优先于 include） | 空 |
| `watch.pollInterval` | 文件系统轮询间隔 | `100ms` |
| `watch.debounce` | 变更合并窗口（窗口内重复通知合并为一次） | `200ms` |
| `build.concurrency` | 最大并行模块构建数（依赖顺序仍受约束） | `1` |
| `build.timeout` | 单个模块构建超时；`0` 表示不限制 | `0` |
| `server.addr` | 监听地址 | `:8080` |

时长字段接受 `250ms`、`2s` 等 Go duration 字符串。

## 更新一致性保证（为什么最终结果与全量构建一致）

1. **合并**：防抖窗口内的重复通知按模块去重，合并为一批。
2. **影响集**：一批变更映射到模块后，经依赖图求「自身 + 全部反向依赖闭包」，
   并按拓扑序构建；无关节点不参与。
3. **构建中变更不丢**：每一轮构建结束后，服务会进入一个完整的静默窗口，期间继续
   收集合并通知；窗口结束后还会对已构建模块做一次**基于内容的复核哈希**。只要
   某模块当前源码与其已发布版本所用源码不一致（哪怕通知已被本轮提前消费），就把
   该模块及其依赖闭包纳入下一轮。如此循环直到真正静默，因此最终产物与从零全量
   构建一致。
4. **去重**：构建键 = 自身源码哈希 + 各依赖版本哈希。相同内容（含相同依赖版本）
   的构建会被跳过，不产生新版本号。
5. **失败隔离**：依赖失败时，下游模块本轮标记为失败并保持上一个成功版本；无依赖
   关系的模块继续可用。修复经第 3 步自动收敛恢复。

## 项目结构

```
cmd/hmr            程序入口（装配 watcher / engine / server）
internal/config    配置加载、默认值、校验
internal/graph     依赖图、拓扑序、受影响闭包
internal/state     模块版本/状态注册表（并发安全、失败保留 last-good）
internal/store     内容寻址、原子替换（临时文件 + fsync + rename）的产物存储
internal/coalescer 泛型防抖合并器（trailing-edge，最后一次不丢）
internal/watcher   零依赖轮询文件监听（范围 / include / exclude）
internal/builder   构建器接口、源码哈希、命令执行
internal/engine    增量构建编排（队列、并发、失败隔离、收敛、取消/超时）
internal/hub       SSE 客户端注册与非阻塞广播
internal/server    HTTP 接口与 SSE
```

## 测试

```bash
go test ./...            # 全部
go test -race ./...      # 竞态检测
```

测试覆盖：依赖传播与拓扑序、保存风暴合并、构建中变更不丢且最终一致、构建失败隔离
与自动恢复、相同内容不产生新版本、并发推送、慢/断开客户端、产物原子替换等。
