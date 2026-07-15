package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/mcp"
)

func mcpClientOptions(workspace string, additionalDirs []string) mcp.ClientOptions {
	paths := append([]string{workspace}, additionalDirs...)
	roots := make([]mcp.Root, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		root, err := mcp.RootForPath(path)
		if err != nil {
			continue
		}
		if _, ok := seen[root.URI]; ok {
			continue
		}
		seen[root.URI] = struct{}{}
		roots = append(roots, root)
	}
	return mcp.ClientOptions{Roots: roots}
}

func currentMCPOptions(provider func() mcp.ClientOptions) mcp.ClientOptions {
	if provider == nil {
		return mcp.ClientOptions{}
	}
	return provider()
}

// NewMCPElicitationHandler adapts Codog's structured question interaction to
// MCP form and URL elicitation requests.
func NewMCPElicitationHandler(questionTool AskUserQuestionTool) func(context.Context, mcp.ElicitationRequest) (mcp.ElicitationResult, error) {
	return func(ctx context.Context, request mcp.ElicitationRequest) (mcp.ElicitationResult, error) {
		if questionTool.In == nil {
			return mcp.ElicitationResult{Action: "decline"}, nil
		}
		if strings.EqualFold(request.Mode, "url") || request.URL != "" {
			return runMCPURLElicitation(ctx, questionTool, request)
		}
		return runMCPFormElicitation(ctx, questionTool, request)
	}
}

func runMCPURLElicitation(ctx context.Context, tool AskUserQuestionTool, request mcp.ElicitationRequest) (mcp.ElicitationResult, error) {
	question := strings.TrimSpace(request.Message)
	if question == "" {
		question = "Allow the MCP server to continue this external interaction?"
	}
	if strings.TrimSpace(request.URL) != "" {
		question += "\n" + strings.TrimSpace(request.URL)
	}
	payload, err := json.Marshal(map[string]any{
		"question": question,
		"choices":  []string{"Accept", "Decline"},
		"default":  "Decline",
	})
	if err != nil {
		return mcp.ElicitationResult{}, err
	}
	output, err := tool.Execute(ctx, payload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return mcp.ElicitationResult{Action: "cancel"}, nil
		}
		return mcp.ElicitationResult{}, err
	}
	var decoded struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal([]byte(output), &decoded) != nil || !strings.EqualFold(strings.TrimSpace(decoded.Answer), "accept") {
		return mcp.ElicitationResult{Action: "decline"}, nil
	}
	return mcp.ElicitationResult{Action: "accept"}, nil
}

func runMCPFormElicitation(ctx context.Context, tool AskUserQuestionTool, request mcp.ElicitationRequest) (mcp.ElicitationResult, error) {
	questions, fields, err := mcpFormQuestions(request)
	if err != nil {
		return mcp.ElicitationResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"questions": questions})
	if err != nil {
		return mcp.ElicitationResult{}, err
	}
	output, err := tool.Execute(ctx, payload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return mcp.ElicitationResult{Action: "cancel"}, nil
		}
		return mcp.ElicitationResult{}, err
	}
	var decoded struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return mcp.ElicitationResult{}, err
	}
	content := make(map[string]any, len(decoded.Answers))
	for question, answer := range decoded.Answers {
		if strings.EqualFold(strings.TrimSpace(answer), "cancel") {
			return mcp.ElicitationResult{Action: "decline"}, nil
		}
		field, ok := fields[question]
		if !ok {
			return mcp.ElicitationResult{}, fmt.Errorf("elicitation returned an unknown field %q", question)
		}
		value, err := coerceMCPFormValue(answer, field.Type)
		if err != nil {
			return mcp.ElicitationResult{}, fmt.Errorf("elicitation field %s: %w", field.Name, err)
		}
		content[field.Name] = value
	}
	return mcp.ElicitationResult{Action: "accept", Content: content}, nil
}

type mcpFormField struct {
	Name string
	Type string
}

func mcpFormQuestions(request mcp.ElicitationRequest) ([]UserQuestion, map[string]mcpFormField, error) {
	properties, ok := request.RequestedSchema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return nil, nil, errors.New("elicitation form has no properties")
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	questions := make([]UserQuestion, 0, len(names))
	fields := make(map[string]mcpFormField, len(names))
	for _, name := range names {
		schema, ok := properties[name].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("elicitation property %s has an invalid schema", name)
		}
		typeName, _ := schema["type"].(string)
		if !supportedMCPFormType(typeName) {
			return nil, nil, fmt.Errorf("elicitation property %s uses unsupported type %q", name, typeName)
		}
		label := name
		if title, _ := schema["title"].(string); strings.TrimSpace(title) != "" {
			label = strings.TrimSpace(title)
		}
		question := label
		if description, _ := schema["description"].(string); strings.TrimSpace(description) != "" {
			question += ": " + strings.TrimSpace(description)
		}
		fields[question] = mcpFormField{Name: name, Type: typeName}
		options := mcpFormOptions(schema, typeName)
		questions = append(questions, UserQuestion{
			Question: question,
			Header:   truncateRunes(label, 12),
			Options:  options,
		})
	}
	return questions, fields, nil
}

func mcpFormOptions(schema map[string]any, typeName string) []UserQuestionOption {
	if values, ok := schema["enum"].([]any); ok {
		options := make([]UserQuestionOption, 0, len(values))
		for _, value := range values {
			label := fmt.Sprint(value)
			options = append(options, UserQuestionOption{Label: label, Description: label})
		}
		if len(options) >= 2 {
			return options
		}
	}
	if typeName == "boolean" {
		return []UserQuestionOption{{Label: "True", Description: "Yes"}, {Label: "False", Description: "No"}}
	}
	return []UserQuestionOption{{Label: "Enter value", Description: "Provide a value"}, {Label: "Cancel", Description: "Decline this request"}}
}

func supportedMCPFormType(typeName string) bool {
	switch typeName {
	case "string", "number", "integer", "boolean":
		return true
	default:
		return false
	}
}

func coerceMCPFormValue(value string, typeName string) (any, error) {
	value = strings.TrimSpace(value)
	switch typeName {
	case "number":
		return strconv.ParseFloat(value, 64)
	case "integer":
		return strconv.ParseInt(value, 10, 64)
	case "boolean":
		return strconv.ParseBool(strings.ToLower(value))
	default:
		return value, nil
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
