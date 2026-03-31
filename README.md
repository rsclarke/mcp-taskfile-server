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
- **Auto Reload**: Watches Taskfile.yml (and included Taskfiles) for changes and automatically re-exposes updated tools to connected clients
- **MCP Protocol Compliance**: Uses the official Go MCP SDK for full specification compliance

## Requirements

- Go 1.19 or later

## Installation

  ```bash
  go get github.com/rsclarke/mcp-taskfile-server
  ```

## Usage

### Running the Server

The server communicates via JSON-RPC over stdin/stdout exposing the Taskfile.yml in the current working directory:

```bash
mcp-taskfile-server
```

### Dynamic Tool Discovery

The server automatically discovers all tasks in your Taskfile.yml and exposes each as an individual MCP tool.

Each tool automatically includes:
- **Task-specific variables**: Extracted from the task definition with proper defaults
- **Proper descriptions**: Uses task descriptions from Taskfile.yml

## Tool Name Mapping

Taskfile task names can contain characters (`:`, `*`) that are invalid in MCP tool names. The server automatically sanitizes task names to conform to the [MCP tool name specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) (`[a-zA-Z0-9_.-]`).

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

When the tool name differs from the original task name, the original is included in the tool description for discoverability.

### Wildcard Tasks

Taskfile [wildcard tasks](https://taskfile.dev/docs/guide#wildcard-arguments) (e.g. `start:*`) are exposed as tools with a required `MATCH` parameter. The server reconstructs the full task name at invocation time.

For a task defined as `start:*`, calling the tool:

```json
{"name": "start", "arguments": {"MATCH": "web"}}
```

executes `task start:web`.

For tasks with multiple wildcards (e.g. `deploy:*:*`), provide comma-separated values:

```json
{"name": "deploy", "arguments": {"MATCH": "api,production"}}
```

executes `task deploy:api:production`.

## MCP Integration

This server implements the Model Context Protocol and can be used with any MCP-compatible client or AI assistant. The server:

1. **Dynamically discovers** all tasks from Taskfile.yml at startup
2. **Sanitizes task names** into valid MCP tool names for strict client compatibility
3. **Exposes each task** as an individual MCP tool with proper JSON schema
4. **Automatically extracts** task variables for parameter validation
5. **Executes tasks natively** using the go-task library (no subprocess calls)
6. **Provides comprehensive** error handling and feedback

## Auto Reload

The server watches your Taskfile.yml and any included Taskfiles for changes using `fsnotify`. When a file is modified, added, or removed, the server automatically:

1. Reloads and re-parses the Taskfile
2. Diffs the updated task set against currently registered tools
3. Adds new tools and removes stale ones via the MCP SDK
4. Notifies connected clients of the change (`notifications/tools/list_changed`)

File system events are debounced (~200 ms) to avoid redundant reloads during rapid edits. The watcher runs for the lifetime of the server and is cleaned up on shutdown.

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

1. **Server Setup**: The MCP server is created using `mcp.NewServer()` from the Go MCP SDK
2. **Dynamic Discovery**: Tasks are discovered and built into a tool set via `buildToolSet()`
3. **Tool Generation**: Each task becomes an MCP tool via `createToolForTask()`
4. **Variable Extraction**: Task variables are automatically extracted for schema generation
5. **Handler Creation**: Each task gets its own handler via `createTaskHandler()`
6. **Tool Sync**: `syncTools()` diffs and updates registered tools; `watchTaskfiles()` triggers reloads on file changes
7. **Native Execution**: Tasks are executed using `executor.Run()` from go-task library

### Key Components

- **`NewTaskfileServer()`**: Sets up go-task executor and parses Taskfile.yml
- **`buildToolSet()`**: Discovers tasks and builds the set of MCP tools and handlers
- **`createToolForTask()`**: Generates MCP tool schema from task definition
- **`createTaskHandler()`**: Creates execution handler for each task
- **`syncTools()`**: Diffs current tasks against registered tools and adds/removes as needed
- **`loadAndRegisterTools()`**: Re-initialises the executor and triggers a sync on reload
- **`watchTaskfiles()`**: Watches Taskfile.yml and included files for changes with debounced reload

### Key Dependencies

- **[Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)**: Official MCP protocol implementation
- **[go-task](https://github.com/go-task/task)**: Native Taskfile.yml parsing and execution
- **[fsnotify](https://github.com/fsnotify/fsnotify)**: Cross-platform file system notifications for auto reload

The server uses the go-task library's native API for both parsing and execution, ensuring maximum compatibility with Taskfile.yml features.
