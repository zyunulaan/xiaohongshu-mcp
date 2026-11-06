package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestSendGroupMessageSchema(t *testing.T) {
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
		if tool.Name == "send_group_message" {
			encoded, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &schema))
			break
		}
	}
	require.NotEmpty(t, schema.Properties)
	resolved, err := schema.Resolve(nil)
	require.NoError(t, err)

	for _, test := range []struct {
		name  string
		input string
		valid bool
	}{
		{"missing_group", `{"text":"x"}`, false},
		{"empty", `{"group_chat":""}`, false},
		{"dry_run_only", `{"group_chat":"群A","dry_run":true}`, true},
		{"text_only", `{"group_chat":"群A","text":"hi"}`, true},
		{"images_only", `{"group_chat":"群A","images":["/tmp/a.png"]}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input any
			require.NoError(t, json.Unmarshal([]byte(test.input), &input))
			if test.valid {
				require.NoError(t, resolved.Validate(input))
			} else {
				require.Error(t, resolved.Validate(input))
			}
		})
	}
}
