package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Rememorio/codog/internal/config"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ListTools discovers every paginated tool while preserving the server session.
func (p *ClientPool) ListTools(ctx context.Context, name string, server config.MCPServerConfig, options ClientOptions) ToolListResult {
	tools, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) ([]*protocol.Tool, error) {
		var tools []*protocol.Tool
		for tool, err := range session.Tools(callCtx, nil) {
			if err != nil {
				return nil, err
			}
			tools = append(tools, tool)
		}
		return tools, nil
	})
	if err != nil {
		return ToolListResult{Server: name, Error: err.Error()}
	}
	result := ToolListResult{Server: name, Tools: make([]ToolInfo, 0, len(tools))}
	for _, tool := range tools {
		schema := map[string]any(nil)
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ToolListResult{Server: name, Error: fmt.Sprintf("encode schema for MCP tool %q: %v", tool.Name, err)}
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return ToolListResult{Server: name, Error: fmt.Sprintf("decode schema for MCP tool %q: %v", tool.Name, err)}
		}
		result.Tools = append(result.Tools, ToolInfo{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return result
}

// CallTool invokes a tool through a persistent server session.
func (p *ClientPool) CallTool(ctx context.Context, name string, server config.MCPServerConfig, toolName string, arguments json.RawMessage, options ClientOptions) ToolCallResult {
	var decoded map[string]any
	if len(arguments) != 0 {
		if err := json.Unmarshal(arguments, &decoded); err != nil {
			return ToolCallResult{Server: name, Tool: toolName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "tool": toolName}), Error: err.Error()}
		}
	}
	result, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) (*protocol.CallToolResult, error) {
		return session.CallTool(callCtx, &protocol.CallToolParams{Name: toolName, Arguments: decoded})
	})
	if err != nil {
		return ToolCallResult{Server: name, Tool: toolName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "tool": toolName}), Error: err.Error()}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return ToolCallResult{Server: name, Tool: toolName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "tool": toolName}), Error: err.Error()}
	}
	return ToolCallResult{Server: name, Tool: toolName, Lifecycle: lifecycleReady("ready"), Result: data}
}

// ListResources discovers every paginated resource through a persistent session.
func (p *ClientPool) ListResources(ctx context.Context, name string, server config.MCPServerConfig, options ClientOptions) ResourceListResult {
	resources, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) ([]*protocol.Resource, error) {
		var resources []*protocol.Resource
		for resource, err := range session.Resources(callCtx, nil) {
			if err != nil {
				return nil, err
			}
			resources = append(resources, resource)
		}
		return resources, nil
	})
	if err != nil {
		return ResourceListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	data, err := json.Marshal(&protocol.ListResourcesResult{Resources: resources})
	if err != nil {
		return ResourceListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	return ResourceListResult{Server: name, Lifecycle: lifecycleReady("ready"), Resources: data}
}

// ReadResource reads one resource through a persistent server session.
func (p *ClientPool) ReadResource(ctx context.Context, name string, server config.MCPServerConfig, uri string, options ClientOptions) ResourceReadResult {
	result, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) (*protocol.ReadResourceResult, error) {
		return session.ReadResource(callCtx, &protocol.ReadResourceParams{URI: uri})
	})
	if err != nil {
		return ResourceReadResult{Server: name, URI: uri, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "uri": uri}), Error: err.Error()}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return ResourceReadResult{Server: name, URI: uri, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "uri": uri}), Error: err.Error()}
	}
	return ResourceReadResult{Server: name, URI: uri, Lifecycle: lifecycleReady("ready"), Result: data}
}

// ListResourceTemplates discovers every paginated template through a persistent session.
func (p *ClientPool) ListResourceTemplates(ctx context.Context, name string, server config.MCPServerConfig, options ClientOptions) ResourceTemplateListResult {
	templates, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) ([]*protocol.ResourceTemplate, error) {
		var templates []*protocol.ResourceTemplate
		for template, err := range session.ResourceTemplates(callCtx, nil) {
			if err != nil {
				return nil, err
			}
			templates = append(templates, template)
		}
		return templates, nil
	})
	if err != nil {
		return ResourceTemplateListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	data, err := json.Marshal(&protocol.ListResourceTemplatesResult{ResourceTemplates: templates})
	if err != nil {
		return ResourceTemplateListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	return ResourceTemplateListResult{Server: name, Lifecycle: lifecycleReady("ready"), Templates: data}
}

// ListPrompts discovers every paginated prompt through a persistent session.
func (p *ClientPool) ListPrompts(ctx context.Context, name string, server config.MCPServerConfig, options ClientOptions) PromptListResult {
	prompts, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) ([]*protocol.Prompt, error) {
		var prompts []*protocol.Prompt
		for prompt, err := range session.Prompts(callCtx, nil) {
			if err != nil {
				return nil, err
			}
			prompts = append(prompts, prompt)
		}
		return prompts, nil
	})
	if err != nil {
		return PromptListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	data, err := json.Marshal(&protocol.ListPromptsResult{Prompts: prompts})
	if err != nil {
		return PromptListResult{Server: name, Lifecycle: lifecycleFailure("resource_discovery", err.Error(), true, map[string]string{"server": name}), Error: err.Error()}
	}
	return PromptListResult{Server: name, Lifecycle: lifecycleReady("ready"), Prompts: data}
}

// GetPrompt renders one prompt through a persistent server session.
func (p *ClientPool) GetPrompt(ctx context.Context, name string, server config.MCPServerConfig, promptName string, arguments json.RawMessage, options ClientOptions) PromptGetResult {
	decoded := map[string]string(nil)
	if len(arguments) != 0 {
		if err := json.Unmarshal(arguments, &decoded); err != nil {
			return PromptGetResult{Server: name, Prompt: promptName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "prompt": promptName}), Error: err.Error()}
		}
	}
	result, err := poolRequest(ctx, p, name, server, options, func(callCtx context.Context, session *protocol.ClientSession) (*protocol.GetPromptResult, error) {
		return session.GetPrompt(callCtx, &protocol.GetPromptParams{Name: promptName, Arguments: decoded})
	})
	if err != nil {
		return PromptGetResult{Server: name, Prompt: promptName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "prompt": promptName}), Error: err.Error()}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return PromptGetResult{Server: name, Prompt: promptName, Lifecycle: lifecycleFailure("invocation", err.Error(), true, map[string]string{"server": name, "prompt": promptName}), Error: err.Error()}
	}
	return PromptGetResult{Server: name, Prompt: promptName, Lifecycle: lifecycleReady("ready"), Result: data}
}
