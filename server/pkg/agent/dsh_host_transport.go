package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

const dshHostMaxFrame = 4 * 1024 * 1024

// This is a transport receipt, not another employee ownership store. The
// launcher obtains these values from PostgreSQL before submitting the task.
type dshHostIdentity struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Generation  int64  `json:"generation"`
}

type dshHostClient struct {
	identity dshHostIdentity
	dial     func(context.Context) (net.Conn, error)
}

type dshHostResponse struct {
	Error json.RawMessage `json:"error"`
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
}

// The supervisor authenticates the employee/generation and keeps the native
// login cookie in its own memory. Neither URLs nor child environments carry it.
func (c *dshHostClient) open(ctx context.Context, method string, request any) (net.Conn, func(), error) {
	body, err := json.Marshal(struct {
		Identity dshHostIdentity `json:"identity"`
		Method   string          `json:"method"`
		Request  any             `json:"request"`
	}{c.identity, method, request})
	if err != nil || len(body)+1 > dshHostMaxFrame {
		return nil, nil, errors.New("invalid DSH Host request")
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, errors.New("DSH Host connection unavailable")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	closeConn := func() { stop(); _ = conn.Close() }
	if err := conn.SetWriteDeadline(time.Now().Add(35 * time.Second)); err != nil {
		closeConn()
		return nil, nil, errors.New("DSH Host write deadline unavailable")
	}
	body = append(body, '\n')
	for len(body) > 0 {
		n, err := conn.Write(body)
		if err != nil || n == 0 {
			closeConn()
			return nil, nil, errors.New("DSH Host request outcome is unknown")
		}
		body = body[n:]
	}
	return conn, closeConn, nil
}

func dshHostRead(conn net.Conn, reader *bufio.Reader, method string) (json.RawMessage, error) {
	// The peer may close after writing several complete frames. Drain frames
	// already buffered before treating a closed transport as interruption.
	if err := conn.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil && reader.Buffered() == 0 {
		return nil, errors.New("DSH Host read deadline unavailable")
	}
	// ReadSlice bounds memory independently of the peer's framing. EOF,
	// truncated records and oversized records never imply task completion.
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > dshHostMaxFrame {
		return nil, errors.New("DSH Host response interrupted or invalid")
	}
	var response dshHostResponse
	if json.Unmarshal(line, &response) != nil {
		return nil, fmt.Errorf("DSH Host %s: invalid response", method)
	}
	if !response.OK {
		return nil, dshHostOperationError(method, response.Error)
	}
	if len(response.Value) == 0 || string(response.Value) == "null" {
		return nil, fmt.Errorf("DSH Host %s: missing response value", method)
	}
	return response.Value, nil
}

func (c *dshHostClient) call(ctx context.Context, method string, request any) (json.RawMessage, error) {
	conn, closeConn, err := c.open(ctx, method, request)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	return dshHostRead(conn, bufio.NewReaderSize(conn, dshHostMaxFrame), method)
}

// The consumer decides completion from the correlated native turn/end event.
// Returning true ends this subscription; it does not cancel the native task.
func (c *dshHostClient) follow(ctx context.Context, request any, consume func(json.RawMessage) (bool, error)) error {
	conn, closeConn, err := c.open(ctx, "follow", request)
	if err != nil {
		return err
	}
	defer closeConn()
	reader := bufio.NewReaderSize(conn, dshHostMaxFrame)
	for {
		value, err := dshHostRead(conn, reader, "follow")
		if err != nil {
			return err
		}
		done, err := consume(value)
		if err != nil || done {
			return err
		}
	}
}

// Decode only bounded protocol categories. Old string errors and arbitrary
// native message/detail fields are deliberately excluded from task output.
func dshHostOperationError(method string, raw json.RawMessage) error {
	var diagnostic struct {
		Code      string `json:"code"`
		Reason    string `json:"reason"`
		ElapsedMs int64  `json:"elapsedMs"`
	}
	_ = json.Unmarshal(raw, &diagnostic)
	code := "host/operation-failed"
	switch diagnostic.Code {
	case "host/rpc-timeout", "host/transport-failed", "host/module-not-found", "host/startup-failed", "multica/context-rejected", "multica/required-mcp-unavailable", "gateway/bad-request", "gateway/cancelled", "gateway/internal",
		"session/agent-busy", "session/attachment-invalid", "session/conflict",
		"session/fork-unavailable", "session/invalid-time-zone", "session/model-unavailable",
		"session/not-found", "session/queue-item-not-found", "session/steer-unavailable",
		"session/title-invalid", "session/workspace-attach-failed", "host/native-rejected":
		code = diagnostic.Code
	}
	summary := "operation rejected"
	switch code {
	case "host/rpc-timeout":
		summary = "native operation deadline exceeded; outcome may still be pending"
	case "host/transport-failed":
		summary = "native transport failed; outcome is unknown"
	case "host/module-not-found":
		summary = "native startup dependency module is missing"
	case "host/startup-failed":
		summary = "native Host failed to start"
	}
	if code == "gateway/internal" && diagnostic.Reason == "history_corrupt" {
		summary = "session history integrity check failed"
	}
	if method == "task.bind" && (diagnostic.Reason == "required_mcp_unavailable" || code == "multica/required-mcp-unavailable") {
		summary = "required MCP startup failed"
	}
	if diagnostic.ElapsedMs > 0 && diagnostic.ElapsedMs < 3600000 {
		return fmt.Errorf("DSH Host %s failed [%s] after %dms: %s", method, code, diagnostic.ElapsedMs, summary)
	}
	return fmt.Errorf("DSH Host %s failed [%s]: %s", method, code, summary)
}
