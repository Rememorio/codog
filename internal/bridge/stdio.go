package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Rememorio/codog/internal/future"
	"github.com/Rememorio/codog/internal/session"
)

type Server struct {
	Sessions *session.Store
	Version  string
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s Server) Serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = encoder.Encode(Response{JSONRPC: "2.0", Error: &Error{Code: -32700, Message: err.Error()}})
			continue
		}
		result, rpcErr := s.handle(req)
		resp := Response{JSONRPC: "2.0", ID: json.RawMessage(req.ID), Result: result, Error: rpcErr}
		_ = encoder.Encode(resp)
	}
	return scanner.Err()
}

func (s Server) handle(req Request) (any, *Error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"name":    "codog",
			"version": s.Version,
			"capabilities": []string{
				"sessions/list",
				"capabilities/list",
			},
		}, nil
	case "capabilities/list":
		return future.NewReport(s.Version), nil
	case "sessions/list":
		sessions, err := s.Sessions.List()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}
		}
		return sessions, nil
	default:
		return nil, &Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
}
