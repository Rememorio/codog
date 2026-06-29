package mockanthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Server struct {
	Text string
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.messages)
	return mux
}

func (s Server) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	text := s.Text
	if text == "" {
		text = "mock response"
	}
	w.Header().Set("content-type", "text/event-stream")
	writeEvent(w, map[string]any{"type": "message_start"})
	writeEvent(w, map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
	for _, token := range strings.Split(text, " ") {
		writeEvent(w, map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": token + " ",
			},
		})
	}
	writeEvent(w, map[string]any{"type": "content_block_stop", "index": 0})
	writeEvent(w, map[string]any{
		"type":  "message_delta",
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	writeEvent(w, map[string]any{"type": "message_stop"})
}

func writeEvent(w http.ResponseWriter, payload map[string]any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", payload["type"], data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
