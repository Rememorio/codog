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

	mu      sync.Mutex
	request int
	attempt int
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

func (s Server) Handler() http.Handler {
	server := &s
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", server.messages)
	return mux
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.OnRequest != nil {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
		s.OnRequest(json.RawMessage(append([]byte(nil), data...)))
	}
	if s.shouldRateLimit() {
		if strings.TrimSpace(s.RetryAfter) != "" {
			w.Header().Set("retry-after", strings.TrimSpace(s.RetryAfter))
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

func (s *Server) shouldRateLimit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempt++
	return s.RateLimitFailures > 0 && s.attempt <= s.RateLimitFailures
}

func (s *Server) nextTurn() Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Turns) == 0 {
		return Turn{Text: s.Text}
	}
	index := s.request
	s.request++
	if index >= len(s.Turns) {
		index = len(s.Turns) - 1
	}
	return s.Turns[index]
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
