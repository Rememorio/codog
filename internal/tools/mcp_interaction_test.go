package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPElicitationHandlerCoercesFormAnswers(t *testing.T) {
	var request UserQuestionRequest
	handler := NewMCPElicitationHandler(AskUserQuestionTool{
		In: strings.NewReader(`{"Count: How many?":"42","Enabled":"true","Mode":"slow"}` + "\n"),
		OnRequest: func(value UserQuestionRequest) {
			request = value
		},
	})
	result, err := handler(context.Background(), mcp.ElicitationRequest{
		Mode:    "form",
		Message: "Configure",
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"Count":   map[string]any{"type": "integer", "description": "How many?"},
				"Enabled": map[string]any{"type": "boolean"},
				"Mode":    map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "accept", result.Action)
	require.EqualValues(t, 42, result.Content["Count"])
	require.Equal(t, true, result.Content["Enabled"])
	require.Equal(t, "slow", result.Content["Mode"])
	require.Len(t, request.Questions, 3)
}

func TestMCPElicitationHandlerDeclinesWithoutInteractiveInput(t *testing.T) {
	handler := NewMCPElicitationHandler(AskUserQuestionTool{})
	result, err := handler(context.Background(), mcp.ElicitationRequest{Mode: "form", Message: "Configure"})
	require.NoError(t, err)
	require.Equal(t, "decline", result.Action)
}

func TestMCPElicitationHandlerAsksBeforeURLInteraction(t *testing.T) {
	handler := NewMCPElicitationHandler(AskUserQuestionTool{In: strings.NewReader("1\n")})
	result, err := handler(context.Background(), mcp.ElicitationRequest{
		Mode: "url", Message: "Authorize access", URL: "https://example.test/authorize",
	})
	require.NoError(t, err)
	require.Equal(t, "accept", result.Action)
}

func TestMCPElicitationHandlerDeclinesURLAndFormCancellation(t *testing.T) {
	urlHandler := NewMCPElicitationHandler(AskUserQuestionTool{In: strings.NewReader("\n")})
	result, err := urlHandler(context.Background(), mcp.ElicitationRequest{Mode: "url", URL: "https://example.test"})
	require.NoError(t, err)
	require.Equal(t, "decline", result.Action)

	formHandler := NewMCPElicitationHandler(AskUserQuestionTool{
		In:        strings.NewReader(`{"Value":"Cancel"}` + "\n"),
		OnRequest: func(UserQuestionRequest) {},
	})
	result, err = formHandler(context.Background(), mcp.ElicitationRequest{Mode: "form", RequestedSchema: map[string]any{
		"properties": map[string]any{"Value": map[string]any{"type": "string"}},
	}})
	require.NoError(t, err)
	require.Equal(t, "decline", result.Action)
}

func TestMCPElicitationHandlerRejectsInvalidForms(t *testing.T) {
	handler := NewMCPElicitationHandler(AskUserQuestionTool{In: strings.NewReader("value\n")})
	_, err := handler(context.Background(), mcp.ElicitationRequest{Mode: "form", RequestedSchema: map[string]any{}})
	require.ErrorContains(t, err, "no properties")
	_, err = handler(context.Background(), mcp.ElicitationRequest{Mode: "form", RequestedSchema: map[string]any{
		"properties": map[string]any{"Items": map[string]any{"type": "array"}},
	}})
	require.ErrorContains(t, err, "unsupported type")
	require.False(t, supportedMCPFormType("array"))
	_, err = coerceMCPFormValue("not-a-number", "integer")
	require.Error(t, err)
}

func TestMCPElicitationHandlerMapsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := NewMCPElicitationHandler(AskUserQuestionTool{In: errorReader{err: context.Canceled}})
	result, err := handler(ctx, mcp.ElicitationRequest{Mode: "url", Message: "Continue?"})
	require.NoError(t, err)
	require.Equal(t, "cancel", result.Action)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
