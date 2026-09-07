# 上游工具接入：zvec-grep 与 CodeGraph

MCPX 通过 `~/.mcpx/.mcp.json`（或 Workspace 级 `.mcp.json` overlay，见 README）挂载上游 stdio MCP Server，GPT 侧统一通过 `mcp_tool` 工具调用（`action=list/describe/call` + `server` + `tool`）。本文记录两个已实测接通的代码检索工具的接入配置。

两个工具首次调用都会经过 MCPX 的扩展确认门（与 Workspace 审批模式无关）：第一次返回 `waiting_confirmation`，向用户确认后以相同业务参数加 `user_confirmed=true` 重试即可执行。上游工具标注为只读时（如 `codegraph_explore`）不触发该门。

## zvec-grep（@zvec/zvec-grep）

本地优先的混合检索（FTS + 向量），npm 包 `@zvec/zvec-grep`，bin 为 `zg`，要求 Node 22+。

```sh
npm install @zvec/zvec-grep   # 或全局安装
zg index                      # 在每个 Workspace 目录内建索引
```

`zg server --stdio` 是给 MCP 客户端的 stdio 桥：它会拉起（或复用）共享 daemon 并把 MCP 代理到 stdin/stdout。daemon 默认绑定 `127.0.0.1:7999`，**每台机器只有一个 daemon**；若 7999 已被其他 ZVEC_GREP_HOME 的 daemon 占用，需单独起 daemon 并用 `ZVEC_GREP_SERVER_URL` 指向它：

```sh
zg server run --listen 127.0.0.1:17999
```

`.mcp.json` 条目：

```json
{
  "mcpServers": {
    "zvec-grep": {
      "type": "stdio",
      "description": "Local-first hybrid workspace search (zvec-grep)",
      "command": "/path/to/zg",
      "args": ["server", "--stdio"],
      "env": {
        "ZVEC_GREP_HOME": "/path/to/zg-home",
        "ZVEC_GREP_SERVER_URL": "http://127.0.0.1:17999/mcp"
      }
    }
  }
}
```

- 机器上没有其他 zg daemon 时，`ZVEC_GREP_SERVER_URL` 可省略（stdio 桥自动拉起 daemon）。
- 索引按 Workspace 存放在 `ZVEC_GREP_HOME` 下；换机器或改 embedding 模型后需重跑 `zg index`。
- 工具面：`zvec_grep_search`（必填参数含 `query`、`root`，`root` 传 Workspace 绝对路径）。
- 实测：经 MCPX 调用返回 fts+vector 混合结果，`operator_control` 上下文正常附加。

## CodeGraph（@colbymchenry/codegraph）

代码知识图谱（tree-sitter 符号图 + 调用关系），npm 包 `@colbymchenry/codegraph`，bin 为 `codegraph`，自带运行时无需外部依赖。

```sh
codegraph init /path/to/project   # 初始化并建索引（生成 .codegraph/，记得加 .gitignore）
```

`.mcp.json` 条目（`serve --mcp` 为 stdio transport，`-p` 固定项目目录）：

```json
{
  "mcpServers": {
    "codegraph": {
      "type": "stdio",
      "description": "Code knowledge graph for codebases (CodeGraph)",
      "command": "/path/to/codegraph",
      "args": ["serve", "--mcp", "-p", "/path/to/project"],
      "env": {}
    }
  }
}
```

- 当前版本（1.6.0）对外只暴露一个 MCP 工具：`codegraph_explore`（一次返回相关符号源码 + 调用路径；标注只读，不触发确认门）。CLI 另有 `query/node/callers/callees/impact` 等命令，但不在 MCP 面上。
- MCPX 调用时 tool 名必须用 `codegraph_explore`，不能用 CLI 命令名。
- 每个项目目录需要各自的 `init`；`codegraph sync` 或内置 watcher 负责增量。
- 实测：经 MCPX 调用返回符号定位与逐字源码，`operator_control` 上下文正常附加。

## 验收记录（2026-09-06）

隔离实例（独立 MCPX_HOME + 端口）实测均通过：

1. `mcp_tool action=list` 列出 `zvec-grep`、`codegraph` 两个上游。
2. `zvec_grep_search` 经确认重试后返回混合检索结果。
3. `codegraph_explore` 直接返回符号图探索结果。
