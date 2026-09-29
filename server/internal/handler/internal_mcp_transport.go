package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const internalMCPMaxResponse = 2 << 20

func internalMCPJSONObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 1 && raw[0] == '{' && raw[len(raw)-1] == '}' && json.Valid(raw)
}

// Stop at the matching SSE response, even when the server keeps the stream open.
func readInternalMCPResponse(body io.Reader, contentType string) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: internalMCPMaxResponse + 1}
	if !strings.HasPrefix(contentType, "text/event-stream") {
		raw, err := io.ReadAll(limited)
		if err != nil || len(raw) > internalMCPMaxResponse {
			return nil, errors.New("response read limit")
		}
		return raw, nil
	}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), internalMCPMaxResponse+1)
	var data []string
	for scanner.Scan() {
		if limited.N == 0 {
			return nil, errors.New("response read limit")
		}
		line := scanner.Text()
		if line == "" {
			payload := []byte(strings.Join(data, "\n"))
			data = nil
			var event struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(payload, &event) == nil && string(event.ID) == "1" {
				return payload, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return nil, errors.New("missing upstream RPC event")
}
