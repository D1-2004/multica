package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

func dshHostRead(conn net.Conn, reader *bufio.Reader) (json.RawMessage, error) {
	if err := conn.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
		return nil, errors.New("DSH Host read deadline unavailable")
	}
	// ReadSlice bounds memory independently of the peer's framing. EOF,
	// truncated records and oversized records never imply task completion.
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > dshHostMaxFrame {
		return nil, errors.New("DSH Host response interrupted or invalid")
	}
	var response dshHostResponse
	if json.Unmarshal(line, &response) != nil || !response.OK || len(response.Value) == 0 || string(response.Value) == "null" {
		return nil, errors.New("DSH Host operation rejected")
	}
	return response.Value, nil
}

func (c *dshHostClient) call(ctx context.Context, method string, request any) (json.RawMessage, error) {
	conn, closeConn, err := c.open(ctx, method, request)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	return dshHostRead(conn, bufio.NewReaderSize(conn, dshHostMaxFrame))
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
		value, err := dshHostRead(conn, reader)
		if err != nil {
			return err
		}
		done, err := consume(value)
		if err != nil || done {
			return err
		}
	}
}
