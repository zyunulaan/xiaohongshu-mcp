package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestPublishWithVideoSchema_CoverOptional(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx := context.Background()
	server := InitMCPServer(&AppServer{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })
	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	var schema jsonschema.Schema
	for _, tool := range tools.Tools {
		if tool.Name == "publish_with_video" {
			encoded, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &schema))
		}
	}
	require.Contains(t, schema.Properties, "cover")
	resolved, err := schema.Resolve(nil)
	require.NoError(t, err)

	validMinimal := `{"title":"t","content":"c","video":"/tmp/v.mp4"}`
	var input any
	require.NoError(t, json.Unmarshal([]byte(validMinimal), &input))
	require.NoError(t, resolved.Validate(input))

	withCover := `{"title":"t","content":"c","video":"/tmp/v.mp4","cover":"/tmp/c.jpg"}`
	require.NoError(t, json.Unmarshal([]byte(withCover), &input))
	require.NoError(t, resolved.Validate(input))
}
