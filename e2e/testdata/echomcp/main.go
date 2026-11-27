// Command echomcp is a minimal stdio MCP server used only by gantline's
// real-Claude tests, so that `/mcp reconnect echomcp` has a target.
package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string `json:"text" jsonschema:"text to echo back"`
}

type echoOut struct {
	Text string `json:"text"`
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "echomcp", Version: "0.1.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo the text back."},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{Text: in.Text}, nil
		})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
