package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	apiPrefix        = "/api/"
	maxAPIBodyBytes  = 1 << 20
	maxAPIErrorBytes = 4 << 10
)

type apiErrorResponse struct {
	Error string `json:"error"`
}

// serveAPITool projects every registered MCP tool onto POST
// /api/{tool-name}. The HTTP and MCP surfaces share the exact handler. Inbound
// authentication remains the consuming deployment's responsibility.
func (s *Server) serveAPITool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if name == "" || strings.Contains(name, "/") {
		writeAPIError(w, http.StatusNotFound, "tool not found")
		return
	}
	handler, ok := s.handlers[name]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "tool not found")
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
	raw, err := decodeAPIArguments(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "request body must be one JSON object")
		return
	}

	result, err := handler(withInboundHeaders(directToolContext(r.Context()), r.Header), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw},
	})
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	if result == nil {
		writeAPIError(w, http.StatusInternalServerError, "tool returned no result")
		return
	}
	if result.Content == nil {
		result.Content = []mcp.Content{}
	}
	if result.IsError {
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(result)
}

func decodeAPIArguments(r io.Reader) (json.RawMessage, error) {
	decoder := json.NewDecoder(r)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON value")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("arguments are not an object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("arguments are not an object")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	if len(message) > maxAPIErrorBytes {
		message = message[:maxAPIErrorBytes]
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorResponse{Error: message})
}
