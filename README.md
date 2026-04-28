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

- Go 1.25 or later

## Installation

```bash
go install github.com/rsclarke/mcp-taskfile-server@latest
```

This places the `mcp-taskfile-server` binary in `$GOBIN` (or `$GOPATH/bin`).

## Usage

### Running the Server

The server communicates via JSON-RPC over stdin/stdout:

```bash
mcp-taskfile-server
```

### Root Discovery

After the client handshake completes, the server calls `roots/list` to discover which directories to load Taskfiles from. Clients that support the MCP [Roots](https://modelcontextprotocol.io/specification/2025-11-25/client/roots) capability can provide one or more `file://` root URIs. If the client does not support roots (returns JSON-RPC `-32601`), the server falls back to the current working directory.

Each root is expected to be the directory that directly contains the top-level Taskfile for that workspace. The server checks only that exact root directory for supported Taskfile filenames and does not walk parent directories.

Equivalent local file URI aliases are canonicalized before they enter server state, so values such as `file:///repo` and `file://localhost/repo` share a single internal root identity.

When roots change at runtime (`notifications/roots/list_changed`), the server diffs the root set, tears down removed roots (cancelling their per-root watcher goroutine and unregistering their tools), and loads any newly added roots.

Roots whose initial Taskfile load fails are kept as **unloaded placeholders**: the workdir is recorded and the root directory is watched for the standard Taskfile filenames so a Taskfile created or fixed after startup is automatically picked up. Placeholder roots expose zero MCP tools.

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

Taskfile [wildcard tasks](https://taskfile.dev/docs/guide#wildcard-arguments) (e.g. `start:*`) are exposed as tools with a required `MATCH` parameter. `MATCH` is a JSON array of strings with one entry per `*` segment in the task name; the schema sets `minItems` and `maxItems` to that count so wrong-arity calls fail validation before the handler runs. The server substitutes each entry into the corresponding `*` segment to reconstruct the full task name at invocation time.

For a task defined as `start:*`, calling the tool:

```json
{"name": "start", "arguments": {"MATCH": ["web"]}}
```

executes `task start:web`.

For tasks with multiple wildcards (e.g. `deploy:*:*`), provide one array element per wildcard segment, in order. Empty strings are rejected:

```json
{"name": "deploy", "arguments": {"MATCH": ["api", "production"]}}
```

executes `task deploy:api:production`.

> **Breaking change**: previous releases accepted `MATCH` as a single comma-separated string (e.g. `"api,production"`). Update clients to send a JSON array of strings instead. Values may now safely contain commas.

## Tool Result Shape

Each task invocation returns a `CallToolResult` with up to three `TextContent` blocks so clients can render or filter streams independently:

1. **Status block** (always present): a one-line summary such as `Task `build` exited with status 0`. Failing tasks surface the underlying exit code reported by `go-task` (e.g. `Task `fail` exited with status 7: ...`); non-exec failures (such as setup errors) fall back to `Task `<name>` failed: <error>`.
2. **Stdout block** (if non-empty): the captured standard output, with `Meta: {"stream": "stdout"}`.
3. **Stderr block** (if non-empty): the captured standard error, with `Meta: {"stream": "stderr"}`.

`IsError` is set to `true` whenever the underlying task returns an error, so clients can react to failure without parsing the status line.

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

The server resolves each root's Taskfile graph using `go-task`, then watches the parent directories for every local Taskfile in that graph using `fsnotify`. Each root has its own watcher goroutine, owned by a per-server `watch.Manager`, so adding or removing a root only spawns or cancels that root's watcher and never disturbs the others. When one of the watched Taskfiles is modified, added, or removed, the server automatically:

1. Reloads and re-parses the root's Taskfile graph
2. Diffs the updated tool set against currently registered tools
3. Adds new tools and removes stale ones via the MCP SDK
4. Notifies connected clients of the change (`notifications/tools/list_changed`)

After each reload, the watcher re-reads the root's watch state so newly included local Taskfiles start being watched and removed ones stop being watched. File system events are debounced (~200 ms) to avoid redundant reloads during rapid edits. Watchers run for the lifetime of the root and are cancelled on root removal or server shutdown.

If a root Taskfile becomes invalid or is deleted, the root is replaced with a fresh placeholder that preserves the workdir and watch set but has no loaded Taskfile, so the root's tools are withdrawn until a valid Taskfile is restored.

## Logging

The server emits structured JSON logs to **stderr** using [`log/slog`](https://pkg.go.dev/log/slog). With the stdio MCP transport, stderr is the only safe channel for diagnostics; stdout is reserved for JSON-RPC traffic.

Each line is a single JSON object with at least:

- `time`, `level`, `msg`
- `service` and `version` of this server
- An `event` field naming the specific occurrence (e.g. `root.load_failed`, `tools.collision`, `watcher.reload_failed`)
- Contextual fields where applicable: `root_uri`, `tool_name`, `error`

The default level is `info`. Set the `MCP_TASKFILE_LOG_LEVEL` environment variable to one of `debug`, `info`, `warn`, or `error` to change it (case-insensitive). Unrecognised values fall back to `info`.

```bash
MCP_TASKFILE_LOG_LEVEL=debug mcp-taskfile-server
```

### MCP Logging Capability

In addition to writing to stderr, the server advertises the MCP [`logging` capability](https://modelcontextprotocol.io/specification/2025-11-25/server/logging) and mirrors every record through the active session as a `notifications/message` notification using the Go SDK's [`mcp.LoggingHandler`](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp#LoggingHandler). The two sinks are independent:

- **Stderr** is the source of truth and is gated by `MCP_TASKFILE_LOG_LEVEL`.
- **MCP forwarding** is gated by the client-set threshold via `logging/setLevel`. Until the client raises the level, the SDK suppresses every record, so connecting clients see no log noise unless they opt in.

The structured attributes (`event`, `root_uri`, `tool_name`, `error`, etc.) are forwarded verbatim as the notification's `data` payload so clients can filter on them. The MCP arm is wired in only after the client handshake completes; records emitted earlier reach stderr alone.

> **Note:** the server is built around a single MCP session per process — `Server.Run` over stdio binds exactly one — so the in-band logging stream is always unambiguously scoped to "this client". Multi-tenant HTTP deployments are out of scope today; if added, the recommended pattern is one `*mcp.Server` per session via [`NewStreamableHTTPHandler`'s `getServer` factory](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp#NewStreamableHTTPHandler), which preserves this 1:1 invariant.

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

### Package Layout

The server is split into small, single-purpose packages under `internal/`:

- **`internal/server`** — Orchestrator. Owns the `*Server` value, the root map, the registered-tool map, the generation counter, and the lifecycle handlers wired into the MCP SDK (`HandleInitialized`, `HandleRootsChanged`).
- **`internal/roots`** — Loads and represents Taskfile roots. Owns the `Root` value type, URI canonicalisation, and Taskfile graph resolution. Does not depend on the MCP SDK.
- **`internal/tools`** — Pure planning. Translates root snapshots into MCP-shaped tools, handles name sanitisation, collision detection, multi-root prefixing, wildcard `MATCH` schemas, and the plan/diff that the orchestrator applies.
- **`internal/exec`** — Per-call task execution handlers, including stdout/stderr capture and the status/stream `TextContent` blocks returned to clients.
- **`internal/watch`** — Per-root fsnotify watcher lifecycle. A `watch.Manager` spawns at most one goroutine per root URI and exposes `Apply` / `Reconcile` / `Shutdown`.
- **`internal/logging`** — Structured logging primitives: stderr handler construction, the MCP `logging` arm, and a `FanoutHandler` that mirrors records to both sinks.

### Lifecycle

1. **Server Setup**: The MCP server is created with `mcp.NewServer()` using `InitializedHandler` and `RootsListChangedHandler`. The orchestrator is constructed with `server.New()` and attached to the MCP server via `SetToolRegistry`.
2. **Logger Wiring**: `SetLogger()` swaps the active `*slog.Logger` atomically; `HandleInitialized()` extends it with the MCP arm bound to the active session so subsequent records reach the client through `notifications/message`.
3. **Root Discovery**: `HandleInitialized()` calls `ListRoots` on the session (falling back to the working directory on `-32601`) and `initializeRoots()` loads them. `HandleRootsChanged()` calls `replaceRoots()` to reconcile the live set against the client's updated list.
4. **Snapshot / Plan / Apply**: `syncTools()` follows a three-phase pattern — snapshot state under lock, call `tools.BuildPlan` without the lock, then re-acquire the lock, validate the generation, and apply the diff to the MCP registry.
5. **Generation Guard**: Each state mutation (root add/remove, root reload, placeholder swap) increments a generation counter. If another mutator runs while a plan is being built, the stale plan is discarded — that mutator will produce its own sync.
6. **Tool Generation**: For each non-internal task, `tools.CreateToolForTask` builds the MCP tool with extracted variables and (for wildcard tasks) a `MATCH` array schema sized to the wildcard count.
7. **Handler Creation**: During planning, each task gets a per-call handler via `exec.NewHandler(workdir, taskName)` bound to the root's workdir.
8. **Native Execution**: The handler builds a fresh `task.Executor` per call (silent mode, captured stdout/stderr) and invokes `executor.Run()` from the go-task library, returning a `CallToolResult` with the status/stdout/stderr blocks described above.
9. **Watching**: `watch.Manager.Apply()` spawns a goroutine per newly added root that runs `watch.Watch()`. On each debounced filesystem event the watcher calls back into the orchestrator via `Server.ReloadRoot()`, which rebuilds the root through `roots.Build()` and triggers another `syncTools()`.

### Key Components

#### `internal/server`

- **`New()`**: Constructs an empty orchestrator with a discard logger and a fresh `watch.Manager`. Roots are loaded after the client handshake.
- **`SetLogger()` / `SetToolRegistry()`**: Wire the structured logger and the MCP server (used as the tool registry) into the orchestrator.
- **`HandleInitialized()`**: Installs the MCP logging arm onto the active logger, then calls `initializeRootsFromSession()`.
- **`HandleRootsChanged()`**: Lists roots from the session, calls `replaceRoots()`, syncs tools, and applies the per-root watcher diff.
- **`initializeRoots()` / `replaceRoots()`**: Reconcile the root map against a desired set of MCP roots. Both return a `reconcileResult` with the added/removed canonical URIs so callers can drive `syncTools()` and `watch.Manager.Apply()` outside the lock.
- **`ReloadRoot()`**: Rebuilds a single root via `roots.Build()` and re-syncs tools. On failure the root is replaced with a placeholder via `disableRootToolsLocked()` so its tools are withdrawn until the Taskfile is restored.
- **`syncTools()`**: Orchestrates the snapshot/plan/apply cycle with generation validation to safely update registered tools.
- **`snapshotToolStateLocked()`**: Captures the current root map and generation under lock for use by `tools.BuildPlan`.
- **`RootWatchState()` / `Shutdown()`**: Implement the `watch.StateProvider` contract; `Shutdown` cancels every per-root watcher and waits for them to exit.

#### `internal/roots`

- **`Build()` / `Load()`**: Resolve a workdir's Taskfile graph, set up a `task.Executor`, and return a populated `*Root`.
- **`NewUnloaded()`**: Returns a placeholder `*Root` for a directory whose Taskfile cannot currently be loaded; the directory is still watched for the standard Taskfile filenames.
- **`CanonicalRootURI()` / `DirToURI()`**: Canonicalise local `file://` URIs so equivalent aliases share one identity.
- **`loadTaskfileWatchSet()`**: Resolves the local Taskfile graph and derives the parent directories and exact Taskfile paths to watch.

#### `internal/tools`

- **`BuildPlan()`**: Computes the desired MCP tool set and handlers from a `StateSnapshot` without mutating the orchestrator; logs and excludes colliding tool names.
- **`CreateToolForTask()`**: Generates the MCP tool definition, schema, and description for a single task.
- **`Diff()`**: Compares old registered tools against desired tools by serialized schema bytes and returns stale/added name lists.
- **Naming helpers**: `RootPrefix()` and the sanitisation helpers implement the rules in [Tool Name Mapping](#tool-name-mapping).

#### `internal/exec`

- **`NewHandler()`**: Returns an `mcp.ToolHandler` bound to a root workdir and task name. The handler resolves wildcard `MATCH` arguments, runs the task with captured streams, and produces the status/stdout/stderr `TextContent` blocks.

#### `internal/watch`

- **`Manager`**: Spawns and tracks per-root watcher goroutines. `Apply()` performs an additive/removal diff; `Reconcile()` converges to a desired URI set; `Shutdown()` cancels every watcher and waits for them to drain.
- **`Watch()`**: Per-root fsnotify loop. Calls `StateProvider.RootWatchState()` for the directories to subscribe to and the Taskfile paths whose modification triggers a debounced reload via `StateProvider.ReloadRoot()`.

#### `internal/logging`

- **`NewLogger()`**: Stderr JSON `*slog.Logger` gated by `MCP_TASKFILE_LOG_LEVEL` and tagged with `service` / `version`.
- **`InstallMCP()`**: Wraps an existing logger so each record is also forwarded as an MCP `notifications/message` on the active session.
- **`FanoutHandler`**: Dispatches a single record to multiple `slog.Handler`s (stderr + MCP) without coupling their lifecycles.

### Key Dependencies

- **[Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)**: Official MCP protocol implementation
- **[go-task](https://github.com/go-task/task)**: Native Taskfile.yml parsing and execution
- **[fsnotify](https://github.com/fsnotify/fsnotify)**: Cross-platform file system notifications for auto reload

The server uses the go-task library's native API for both parsing and execution, ensuring maximum compatibility with Taskfile.yml features.
