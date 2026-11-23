package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xilen0x/linux-diag-mcp/internal/server"
)

// connect wires a server built by server.New to an in-memory MCP client.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	srv := server.New(server.Options{Version: "test"})
	serverT, clientT := mcp.NewInMemoryTransports()

	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestInitializeReportsServerIdentity(t *testing.T) {
	cs := connect(t)
	info := cs.InitializeResult().ServerInfo
	if info.Name != server.Name || info.Version != "test" {
		t.Fatalf("server info = %+v, want name %q version %q", info, server.Name, "test")
	}
}

func TestToolsListIsEmpty(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != 0 {
		t.Fatalf("tools/list returned %d tools, want 0", len(res.Tools))
	}
}
