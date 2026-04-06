# MCP Taskfile Server

A Model Context Protocol (MCP) server that dynamically exposes Taskfile.yml tasks as individual MCP tools, allowing AI assistants to discover and execute any task defined in your Taskfile.

Built using the official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) for MCP protocol implementation and the [go-task](https://github.com/go-task/task) library for native Taskfile.yml parsing and execution.

## Why

- Standard practices for building, linting, etc. are already defined in a Taskfile.  Allow the assistant to execute these tasks directly.
- Parity between local, CI and AI.
- Seemed like a fun idea.

## Features

- **Dynamic Task Discovery**: Automatically discovers all tasks from Taskfile.yml at runtime
- **Individual Task Tools**: Each task becomes its own MCP tool with proper schema
- **Variable Schema Generation**: Automatically extracts task variables for proper parameter validation
- **Native Task Execution**: Uses go-task library directly (no subprocess execution)
- **Multi-Root Support**: Discovers roots via the MCP [Roots](https://modelcontextprotocol.io/specification/2025-11-25/client/roots) capability, loading Taskfiles from each root directory
- **Auto Reload**: Watches each root's resolved local Taskfile graph for changes and automatically re-exposes updated tools to connected clients
- **MCP Protocol Compliance**: Uses the official Go MCP SDK for full specification compliance

## Requirements

- Go 1.19 or later

## Installation

  ```bash
  go get github.com/rsclarke/mcp-taskfile-server
  ```

## Usage

### Running the Server

The server communicates via JSON-RPC over stdin/stdout:

```bash
mcp-taskfile-server
```

### Root Discovery

After the client handshake completes, the server calls `roots/list` to discover which directories to load Taskfiles from. Clients that support the MCP [Roots](https://modelcontextprotocol.io/specification/2025-11-25/client/roots) capability can provide one or more `file://` root URIs. If the client does not support roots (returns JSON-RPC `-32601`), the server falls back to the current working directory.

Equivalent local file URI aliases are canonicalized before they enter server state, so values such as `file:///repo` and `file://localhost/repo` share a single internal root identity.

When roots change at runtime (`notifications/roots/list_changed`), the server automatically diffs the root set, tears down removed roots (stopping their file watchers and unregistering their tools), and loads any newly added roots.

### Dynamic Tool Discovery

The server automatically discovers all tasks in each root's Taskfile.yml and exposes each as an individual MCP tool.
Roots that contain no public tasks are still loaded successfully; they simply expose zero MCP tools until public tasks appear.

Each tool automatically includes:
- **Task-specific variables**: Extracted from the task definition with proper defaults
- **Proper descriptions**: Uses task descriptions from Taskfile.yml

## Tool Name Mapping

Taskfile task names can contain characters such as `:`, `*`, spaces, `/`, and non-ASCII text that are not MCP-valid tool names. The server automatically sanitizes exported tool names to conform to the [MCP tool name specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) (`[a-zA-Z0-9_.-]`, max length `128`).

### Naming Rules

| Transformation | Example task name | MCP tool name |
|---|---|---|
| Colons → underscores | `db:migrate` | `db_migrate` |
| Namespace (includes) | `docs:serve` | `docs_serve` |
| Deep namespacing | `uv:run:dev:lint-imports` | `uv_run_dev_lint-imports` |
| Leading dot preserved | `uv:.venv` | `uv_.venv` |
| Single wildcard | `start:*` | `start` |
| Multiple wildcards | `deploy:*:*` | `deploy` |
| Mixed namespace + wildcard | `uv:add:*` | `uv_add` |
| Slash → underscore | `build/dev` | `build_dev` |
| Space → underscore | `release prod` | `release_prod` |
| Non-ASCII → underscore | `café` | `caf_` |

When the tool name differs from the original task name, the original is included in the tool description for discoverability.
If a sanitized tool name would exceed 128 characters, it is truncated and given a deterministic hash suffix.
If multiple tasks resolve to the same final MCP tool name after sanitization and optional root prefixing, all of those colliding tasks are excluded from MCP exposure.

### Multi-Root Prefixing

With a **single root**, tool names are unprefixed (as shown above). When the client provides **multiple roots**, each tool name is prefixed with a sanitized form of the root directory's basename to avoid collisions:

| Root directory | Task | MCP tool name |
|---|---|---|
| `/home/user/frontend` | `build` | `frontend_build` |
| `/home/user/backend` | `build` | `backend_build` |
| `/home/user/frontend` | `lint:*` | `frontend_lint` |

The prefix is derived from the directory basename with non-alphanumeric characters (except `_`, `-`, `.`) replaced by underscores. If a root is added or removed such that the count crosses the 1↔N boundary, all tools are re-registered with or without prefixes accordingly.

### Wildcard Tasks

Taskfile [wildcard tasks](https://taskfile.dev/docs/guide#wildcard-arguments) (e.g. `start:*`) are exposed as tools with a required `MATCH` parameter. The server reconstructs the full task name at invocation time.

For a task defined as `start:*`, calling the tool:

```json
{"name": "start", "arguments": {"MATCH": "web"}}
```

executes `task start:web`.

For tasks with multiple wildcards (e.g. `deploy:*:*`), provide exactly one comma-separated value per wildcard segment. Surrounding whitespace is trimmed, but empty segments are rejected:

```json
{"name": "deploy", "arguments": {"MATCH": "api,production"}}
```

executes `task deploy:api:production`.

## MCP Integration

This server implements the Model Context Protocol and can be used with any MCP-compatible client or AI assistant. The server:

1. **Requests roots** from the client after handshake; falls back to the working directory if unsupported
2. **Dynamically discovers** all tasks from each root's Taskfile.yml
3. **Sanitizes task names** into valid MCP tool names for strict client compatibility
4. **Exposes each task** as an individual MCP tool with proper JSON schema
5. **Automatically extracts** task variables for parameter validation
6. **Reacts to root changes** by adding/removing roots and re-syncing tools at runtime
7. **Executes tasks natively** using the go-task library (no subprocess calls)
8. **Provides comprehensive** error handling and feedback

## Auto Reload

The server resolves each root's Taskfile graph using `go-task`, then watches the parent directories for every local Taskfile in that graph using `fsnotify`. When one of those Taskfiles is modified, added, or removed, the server automatically:

1. Reloads and re-parses the Taskfile
2. Diffs the updated task set against currently registered tools
3. Adds new tools and removes stale ones via the MCP SDK
4. Notifies connected clients of the change (`notifications/tools/list_changed`)

After each reload, the server recomputes the graph-derived watch set so newly included local Taskfiles start being watched and removed ones stop being watched. File system events are debounced (~200 ms) to avoid redundant reloads during rapid edits. The watcher runs for the lifetime of the server and is cleaned up on shutdown.
If a root Taskfile becomes invalid or is deleted, the server withdraws that root's tools until a valid Taskfile is restored.

## Error Handling

The server handles various error conditions:
- Missing Taskfile.yml
- Invalid task names
- Task execution failures
- Invalid MCP requests

All errors are returned following MCP error response format.

## Security Considerations

This server executes arbitrary commands defined in your Taskfile. Only use it in trusted environments and ensure your Taskfile doesn't contain malicious commands.

## Development

To modify or extend the server:

1. **Server Setup**: The MCP server is created using `mcp.NewServer()` with `InitializedHandler` and `RootsListChangedHandler`
2. **Root Loading**: `HandleInitialized()` calls `ListRoots` to discover directories; `HandleRootsChanged()` diffs and updates the root set via `reconcileRoots()`
3. **Snapshot/Plan/Apply**: `syncTools()` follows a three-phase pattern — snapshot state under lock, build a tool plan without the lock, then re-acquire the lock to validate the generation and apply changes
4. **Generation Guard**: Each state mutation increments a generation counter; if another mutator runs concurrently, the stale plan is discarded without touching the MCP server
5. **Tool Generation**: Each task becomes an MCP tool via `createToolForTask()`
6. **Variable Extraction**: Task variables are automatically extracted for schema generation
7. **Handler Creation**: During tool planning, each task gets a per-call handler via `createTaskHandlerForWorkdir()`
8. **Native Execution**: Tasks are executed using `executor.Run()` from go-task library

### Key Components

- **`New()`**: Creates an empty server; roots are loaded after the client handshake
- **`HandleInitialized()`**: Requests roots from the client, loads each root's Taskfile, syncs tools, and starts file watchers
- **`HandleRootsChanged()`**: Diffs the current root set against the client's updated list, adding/removing roots and re-syncing tools
- **`reconcileRoots()`**: Canonicalizes incoming roots, loads new ones, removes stale ones, bumps the generation, syncs tools, and restarts watchers
- **`loadRoot()` / `unloadRoot()`**: Loads or removes per-root Taskfile data and watch-set state
- **`loadTaskfileWatchSet()`**: Resolves the local Taskfile graph and derives the Taskfiles and parent directories to watch
- **`snapshotToolStateLocked()`**: Captures the current root map and generation under lock for use by `buildToolPlan()`
- **`buildToolPlan()`**: Computes the desired MCP tool set and handlers from a snapshot without mutating the server
- **`createToolForTask()`**: Generates MCP tool schema from task definition
- **`createTaskHandlerForWorkdir()`**: Creates per-call execution handlers bound to a root workdir
- **`diffTools()`**: Compares old registered tools against desired tools and returns stale/added lists
- **`syncTools()`**: Orchestrates the snapshot/plan/apply cycle with generation validation to safely update registered tools
- **`watchTaskfiles()`**: Watches the graph-derived Taskfile set for changes with debounced reload

### Key Dependencies

- **[Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)**: Official MCP protocol implementation
- **[go-task](https://github.com/go-task/task)**: Native Taskfile.yml parsing and execution
- **[fsnotify](https://github.com/fsnotify/fsnotify)**: Cross-platform file system notifications for auto reload

The server uses the go-task library's native API for both parsing and execution, ensuring maximum compatibility with Taskfile.yml features.
