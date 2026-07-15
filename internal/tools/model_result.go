package tools

import (
	"encoding/json"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
)

type modelResultMapper interface {
	ModelResult(string) (string, []anthropic.ContentBlock)
}

// ModelResult converts a tool's transport output into model-visible text and
// optional rich content. Tools without a mapper retain their original output.
func (r *Registry) ModelResult(name string, output string) (string, []anthropic.ContentBlock) {
	_, tool, ok := r.resolve(name)
	if !ok {
		return output, nil
	}
	mapper, ok := tool.(modelResultMapper)
	if !ok {
		return output, nil
	}
	return mapper.ModelResult(output)
}

// ModelResult turns read_file image and PDF payloads into provider-native
// content blocks while keeping only compact metadata in the transcript.
func (ReadFileTool) ModelResult(output string) (string, []anthropic.ContentBlock) {
	var result struct {
		Kind      string `json:"kind"`
		Path      string `json:"path"`
		Bytes     int    `json:"bytes"`
		MediaType string `json:"media_type"`
		Base64    string `json:"base64"`
		Width     int    `json:"width,omitempty"`
		Height    int    `json:"height,omitempty"`
	}
	if json.Unmarshal([]byte(output), &result) != nil || strings.TrimSpace(result.Base64) == "" {
		return output, nil
	}
	metadata := map[string]any{
		"kind": result.Kind, "path": result.Path, "bytes": result.Bytes, "media_type": result.MediaType,
	}
	if result.Width > 0 {
		metadata["width"] = result.Width
	}
	if result.Height > 0 {
		metadata["height"] = result.Height
	}
	block := anthropic.ContentBlock{
		Title: result.Path,
		Source: &anthropic.ContentSource{
			Type: "base64", MediaType: result.MediaType, Data: result.Base64,
		},
	}
	switch result.Kind {
	case "image":
		if !modelImageMediaType(result.MediaType) {
			return output, nil
		}
		block.Type = "image"
	case "document":
		if result.MediaType != "application/pdf" {
			return output, nil
		}
		block.Type = "document"
	default:
		return output, nil
	}
	return pretty(metadata), []anthropic.ContentBlock{block}
}

func modelImageMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/gif", "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}
