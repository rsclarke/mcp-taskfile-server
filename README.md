# MCP Taskfile Server

A Model Context Protocol (MCP) server that dynamically exposes Taskfile.yml tasks as individual MCP tools, allowing AI assistants to discover and execute any task defined in your Taskfile.

Built using the [mcp-go](https://github.com/mark3labs/mcp-go) library for robust MCP protocol implementation and the [go-task](https://github.com/go-task/task) library for native Taskfile.yml parsing and execution.

## Why

- Standard practices for building, linting, etc. are already defined in a Taskfile.  Allow the assistant to execute these tasks directly.
- Parity between local, CI and AI.
- Seemed like a fun idea.

## Features

- **Dynamic Task Discovery**: Automatically discovers all tasks from Taskfile.yml at runtime
- **Individual Task Tools**: Each task becomes its own MCP tool with proper schema
- **Variable Schema Generation**: Automatically extracts task variables for proper parameter validation
- **Native Task Execution**: Uses go-task library directly (no subprocess execution)
- **MCP Protocol Compliance**: Uses mcp-go library for full MCP specification compliance
- **High-level API**: Built with proven libraries for clean, maintainable code

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

## MCP Integration

This server implements the Model Context Protocol and can be used with any MCP-compatible client or AI assistant. The server:

1. **Dynamically discovers** all tasks from Taskfile.yml at startup
2. **Exposes each task** as an individual MCP tool with proper JSON schema
3. **Automatically extracts** task variables for parameter validation
4. **Executes tasks natively** using the go-task library (no subprocess calls)
5. **Provides comprehensive** error handling and feedback

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

1. **Server Setup**: The MCP server is created using `server.NewMCPServer()` from mcp-go
2. **Dynamic Discovery**: Tasks are discovered via `taskfile.Tasks.All()` from the go-task library
3. **Tool Generation**: Each task becomes an MCP tool via `createToolForTask()`
4. **Variable Extraction**: Task variables are automatically extracted for schema generation
5. **Handler Creation**: Each task gets its own handler via `createTaskHandler()`
6. **Native Execution**: Tasks are executed using `executor.Run()` from go-task library

### Key Components

- **`NewTaskfileServer()`**: Sets up go-task executor and parses Taskfile.yml
- **`registerTasks()`**: Discovers tasks and registers them with MCP server
- **`createToolForTask()`**: Generates MCP tool schema from task definition
- **`createTaskHandler()`**: Creates execution handler for each task

### Key Dependencies

- **[mcp-go](https://github.com/mark3labs/mcp-go)**: High-level MCP protocol implementation
- **[go-task](https://github.com/go-task/task)**: Native Taskfile.yml parsing and execution

The server uses the go-task library's native API for both parsing and execution, ensuring maximum compatibility with Taskfile.yml features.
