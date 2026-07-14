package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/usage"
)

func acpStdioScenario() scenario {
	return scenario{
		name:     "acp_stdio_roundtrip",
		runLocal: acpStdioScenarioRunLocal,
	}
}

func decodeLocalJSONRPCResponses(output string) ([]map[string]any, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func usageSummaryForResult(result runloop.TurnResult) usage.Summary {
	values := make([]anthropic.Usage, 0, len(result.MessageUsages))
	for _, messageUsage := range result.MessageUsages {
		values = append(values, messageUsage.Usage)
	}
	if summary, ok := usage.ActualSummary(values, "mock"); ok {
		return summary
	}
	return usage.Estimate(result.Messages, "mock")
}

func addUsageSummary(total, next usage.Summary) usage.Summary {
	total.InputTokens += next.InputTokens
	total.OutputTokens += next.OutputTokens
	total.CacheCreationInputTokens += next.CacheCreationInputTokens
	total.CacheReadInputTokens += next.CacheReadInputTokens
	total.TotalTokens += next.TotalTokens
	total.EstimatedUSD = math.Round((total.EstimatedUSD+next.EstimatedUSD)*100000) / 100000
	switch {
	case total.Source == "":
		total.Source = next.Source
	case next.Source != "" && total.Source != next.Source:
		total.Source = "mixed"
	}
	return total
}

func requestMessageCounts(requests []anthropic.Request) []int {
	if len(requests) == 0 {
		return nil
	}
	counts := make([]int, 0, len(requests))
	for _, request := range requests {
		counts = append(counts, len(request.Messages))
	}
	return counts
}

func compactRequestCount(requests []anthropic.Request) int {
	count := 0
	for _, request := range requests {
		if len(request.Messages) == 0 || len(request.Messages[0].Content) == 0 {
			continue
		}
		if strings.Contains(request.Messages[0].Content[0].Text, "auto-compacted") {
			count++
		}
	}
	return count
}

func registryForScenario(workspace string, configHome string, item scenario) (*tools.Registry, error) {
	options := tools.RegistryOptions{ConfigHome: configHome}
	if item.registryOptions != nil {
		options = item.registryOptions(workspace, configHome)
		if options.ConfigHome == "" {
			options.ConfigHome = configHome
		}
	}
	registry := tools.NewRegistryWithOptions(workspace, options)
	if item.configureRegistry != nil {
		if err := item.configureRegistry(registry); err != nil {
			return nil, err
		}
	}
	if !item.plugins {
		return registry, nil
	}
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return nil, err
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, tool := range manifest.Tools {
			if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Command) == "" {
				continue
			}
			if registry.Has(tool.Name) {
				return nil, fmt.Errorf("plugin tool %q conflicts with an existing tool", tool.Name)
			}
			registry.Register(tools.CommandTool{
				Name:        tool.Name,
				Description: tool.Description,
				Schema:      tool.InputSchema,
				Required:    tools.Permission(tool.Permission),
				Command:     tool.Command,
				Args:        tool.Args,
				Workspace:   manifest.Root,
			})
		}
	}
	return registry, nil
}

func expectToolCalls(result runloop.TurnResult, count int, wantError bool) error {
	if len(result.ToolCalls) != count {
		return fmt.Errorf("expected %d tool calls, got %d", count, len(result.ToolCalls))
	}
	for _, call := range result.ToolCalls {
		if call.IsError != wantError {
			return fmt.Errorf("tool %s error=%t, want %t; output=%s", call.Name, call.IsError, wantError, call.Output)
		}
	}
	return nil
}

func acpStdioScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"status","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	err := acpserver.Serve(ctx, strings.NewReader(input), &out, acpserver.Handlers{
		Status: func(context.Context) (any, error) {
			return map[string]any{"kind": "acp", "status": "ok", "workspace": workspace}, nil
		},
		NewSession: func(context.Context) (acpserver.SessionInfo, error) {
			return acpserver.SessionInfo{SessionID: "acp-harness-session", Workspace: workspace}, nil
		},
	}, acpserver.Options{Version: "harness", Workspace: workspace})
	if err != nil {
		return localScenarioResult{}, err
	}
	responses, err := decodeLocalJSONRPCResponses(out.String())
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(responses) != 4 {
		return localScenarioResult{}, fmt.Errorf("expected 4 ACP responses, got %d", len(responses))
	}
	initialize, ok := responses[0]["result"].(map[string]any)
	if !ok {
		return localScenarioResult{}, fmt.Errorf("initialize response missing result")
	}
	serverInfo, ok := initialize["serverInfo"].(map[string]any)
	if !ok || serverInfo["version"] != "harness" {
		return localScenarioResult{}, fmt.Errorf("initialize serverInfo missing harness version: %#v", initialize["serverInfo"])
	}
	capabilities, ok := initialize["capabilities"].(map[string]any)
	if !ok || capabilities["prompt"] != true {
		return localScenarioResult{}, fmt.Errorf("initialize capabilities missing prompt support: %#v", initialize["capabilities"])
	}
	status, ok := responses[1]["result"].(map[string]any)
	if !ok || status["status"] != "ok" {
		return localScenarioResult{}, fmt.Errorf("status response was not ok: %#v", responses[1]["result"])
	}
	sessionResult, ok := responses[2]["result"].(map[string]any)
	if !ok || sessionResult["session_id"] != "acp-harness-session" {
		return localScenarioResult{}, fmt.Errorf("session/new response missing session id: %#v", responses[2]["result"])
	}
	if _, ok := responses[3]["result"]; !ok {
		return localScenarioResult{}, fmt.Errorf("shutdown response missing result: %#v", responses[3])
	}

	message := "acp stdio harness ok"
	return localScenarioResult{
		Output:       strings.TrimSpace(out.String()),
		FinalMessage: message,
		RequestCount: len(responses),
		MessageCount: len(responses),
	}, nil
}
