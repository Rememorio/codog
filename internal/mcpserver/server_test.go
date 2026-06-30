package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestServeListsAndCallsTools(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("note body"), 0o644))
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"note.txt"}}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	require.NoError(t, Serve(context.Background(), strings.NewReader(input), &out, tools.NewRegistry(workspace), Options{
		Version:        "test",
		PermissionMode: string(tools.PermissionWorkspace),
	}))

	responses := decodeResponses(t, out.String())
	require.Len(t, responses, 3)
	require.Equal(t, "test", responses[0]["result"].(map[string]any)["serverInfo"].(map[string]any)["version"])
	toolsPayload := responses[1]["result"].(map[string]any)["tools"].([]any)
	require.NotEmpty(t, toolsPayload)
	toolNames := toolNamesFromPayload(toolsPayload)
	require.Contains(t, toolNames, "read_file")
	require.NotContains(t, toolNames, "ask_user_question")
	callResult := responses[2]["result"].(map[string]any)
	content := callResult["content"].([]any)[0].(map[string]any)
	require.Equal(t, "text", content["type"])
	require.Contains(t, content["text"], "note body")
}

func TestServeExposesResourcesAndPrompts(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("note body"), 0o644))
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"codog://workspace"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"codog://file/note.txt"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/templates/list","params":{}}`,
		`{"jsonrpc":"2.0","id":6,"method":"prompts/list","params":{}}`,
		`{"jsonrpc":"2.0","id":7,"method":"prompts/get","params":{"name":"summarize_file","arguments":{"path":"note.txt"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/read","params":{"uri":"codog://file/../secret.txt"}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	require.NoError(t, Serve(context.Background(), strings.NewReader(input), &out, tools.NewRegistry(workspace), Options{
		Version:        "test",
		Workspace:      workspace,
		PermissionMode: string(tools.PermissionWorkspace),
	}))

	responses := decodeResponses(t, out.String())
	require.Len(t, responses, 8)
	capabilities := responses[0]["result"].(map[string]any)["capabilities"].(map[string]any)
	require.NotNil(t, capabilities["resources"])
	require.NotNil(t, capabilities["prompts"])

	resources := responses[1]["result"].(map[string]any)["resources"].([]any)
	require.Contains(t, resourceURIs(resources), "codog://workspace")
	require.Contains(t, resourceURIs(resources), "codog://tools")

	workspaceContent := responses[2]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	require.Equal(t, "application/json", workspaceContent["mimeType"])
	require.Contains(t, workspaceContent["text"], workspace)

	fileContent := responses[3]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	require.Equal(t, "text/plain", fileContent["mimeType"])
	require.Equal(t, "note body", fileContent["text"])

	templates := responses[4]["result"].(map[string]any)["resourceTemplates"].([]any)
	require.Equal(t, "codog://file/{path}", templates[0].(map[string]any)["uriTemplate"])

	prompts := responses[5]["result"].(map[string]any)["prompts"].([]any)
	require.Contains(t, promptNames(prompts), "summarize_file")

	prompt := responses[6]["result"].(map[string]any)["messages"].([]any)[0].(map[string]any)
	content := prompt["content"].(map[string]any)
	require.Contains(t, content["text"], "note.txt")

	errPayload := responses[7]["error"].(map[string]any)
	require.EqualValues(t, -32602, errPayload["code"])
	require.Contains(t, errPayload["message"], "escapes workspace")
}

func TestServeReturnsToolErrorsAsMCPContent(t *testing.T) {
	workspace := t.TempDir()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"path":"note.txt","content":"x"}}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	require.NoError(t, Serve(context.Background(), strings.NewReader(input), &out, tools.NewRegistry(workspace), Options{
		PermissionMode: string(tools.PermissionReadOnly),
	}))

	responses := decodeResponses(t, out.String())
	require.Len(t, responses, 1)
	result := responses[0]["result"].(map[string]any)
	require.Equal(t, true, result["isError"])
	content := result["content"].([]any)[0].(map[string]any)
	require.Contains(t, content["text"], "permission denied")
}

func TestServeRejectsInteractiveTools(t *testing.T) {
	workspace := t.TempDir()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user_question","arguments":{"question":"Continue?"}}}`,
		"",
	}, "\n")
	var out bytes.Buffer

	require.NoError(t, Serve(context.Background(), strings.NewReader(input), &out, tools.NewRegistry(workspace), Options{}))

	responses := decodeResponses(t, out.String())
	require.Len(t, responses, 1)
	errPayload := responses[0]["error"].(map[string]any)
	message, _ := errPayload["message"].(string)
	require.Contains(t, message, "not exposed")
}

func decodeResponses(t *testing.T, output string) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		responses = append(responses, response)
	}
	return responses
}

func toolNamesFromPayload(values []any) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		tool, _ := value.(map[string]any)
		if name, _ := tool["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func resourceURIs(values []any) []string {
	uris := make([]string, 0, len(values))
	for _, value := range values {
		resource, _ := value.(map[string]any)
		if uri, _ := resource["uri"].(string); uri != "" {
			uris = append(uris, uri)
		}
	}
	return uris
}

func promptNames(values []any) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		prompt, _ := value.(map[string]any)
		if name, _ := prompt["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}
