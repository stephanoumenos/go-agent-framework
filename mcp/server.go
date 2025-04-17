package mcp

import (
	"github.com/mark3labs/mcp-go/server"
)

type MCPServer server.MCPServer // the underlying mark3labs implementation

func NewMCPServer(s *server.MCPServer, tools ...MCPTool) *MCPServer {
	serverTools := make([]server.ServerTool, 0, len(tools))
	for _, tool := range tools {
		serverTools = append(serverTools, server.ServerTool{
			Tool:    tool.Definition(),
			Handler: tool.handleRequest,
		})
	}
	s.AddTools(serverTools...)
	return (*MCPServer)(s)
}
