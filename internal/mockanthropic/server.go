package mockanthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Server struct {
	Text      string
	Turns     []Turn
	OnRequest func(json.RawMessage)

	RateLimitFailures int
	RetryAfter        string
}

type Turn struct {
	Text     string
	ToolUses []ToolUse
}

type ToolUse struct {
	ID          string
	Name        string
	Input       json.RawMessage
	InputDeltas []string
}

type handlerState struct {
	config  Server
	mu      sync.Mutex
	request int
	attempt int
}

func (s Server) Handler() http.Handler {
	state := &handlerState{config: s}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", state.messages)
	return mux
}

func (s *handlerState) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.config.OnRequest != nil {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
		s.config.OnRequest(json.RawMessage(append([]byte(nil), data...)))
	}
	if s.shouldRateLimit() {
		if strings.TrimSpace(s.config.RetryAfter) != "" {
			w.Header().Set("retry-after", strings.TrimSpace(s.config.RetryAfter))
		}
		http.Error(w, "mock rate limit", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	turn := s.nextTurn()
	if turn.Text == "" && len(turn.ToolUses) == 0 {
		turn.Text = "mock response"
	}
	writeEvent(w, map[string]any{"type": "message_start"})
	index := 0
	if turn.Text != "" {
		writeTextBlock(w, index, turn.Text)
		index++
	}
	for _, toolUse := range turn.ToolUses {
		input := toolUse.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		if len(toolUse.InputDeltas) > 0 {
			input = json.RawMessage(`null`)
		}
		writeEvent(w, map[string]any{
			"type":  "content_block_start",
			"index": index,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    toolUse.ID,
				"name":  toolUse.Name,
				"input": input,
			},
		})
		for _, delta := range toolUse.InputDeltas {
			writeEvent(w, map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": delta,
				},
			})
		}
		writeEvent(w, map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	writeEvent(w, map[string]any{
		"type":  "message_delta",
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	writeEvent(w, map[string]any{"type": "message_stop"})
}

func (s *handlerState) shouldRateLimit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempt++
	return s.config.RateLimitFailures > 0 && s.attempt <= s.config.RateLimitFailures
}

func (s *handlerState) nextTurn() Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.config.Turns) == 0 {
		return Turn{Text: s.config.Text}
	}
	index := s.request
	s.request++
	if index >= len(s.config.Turns) {
		index = len(s.config.Turns) - 1
	}
	return s.config.Turns[index]
}

func writeTextBlock(w http.ResponseWriter, index int, text string) {
	writeEvent(w, map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
	for _, token := range strings.Split(text, " ") {
		writeEvent(w, map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{
				"type": "text_delta",
				"text": token + " ",
			},
		})
	}
	writeEvent(w, map[string]any{"type": "content_block_stop", "index": index})
}

func writeEvent(w http.ResponseWriter, payload map[string]any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", payload["type"], data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
