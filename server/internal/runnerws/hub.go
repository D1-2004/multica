package runnerws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

const (
	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingPeriod      = 45 * time.Second
	maxMessageBytes = 2 << 20
)

type Identity struct {
	MachineID    string
	ConnectionID string
}

type ConnectHandler func(context.Context, Identity)
type DisconnectHandler func(Identity)
type HeartbeatHandler func(context.Context, Identity, runnerprotocol.Heartbeat)
type ResultHandler func(context.Context, Identity, runnerprotocol.Result)
type InventoryHandler func(context.Context, Identity, runnerprotocol.MCPInventory)

type client struct {
	hub      *Hub
	conn     *websocket.Conn
	identity Identity
	send     chan outboundFrame
	done     chan struct{}
	close    sync.Once
}

type outboundFrame struct {
	payload    []byte
	closeAfter bool
}

func (c *client) stop() {
	c.close.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

type Hub struct {
	upgrader websocket.Upgrader

	mu        sync.RWMutex
	byMachine map[string]*client
	inventory map[string]runnerprotocol.MCPInventory

	handlerMu    sync.RWMutex
	onConnect    ConnectHandler
	onDisconnect DisconnectHandler
	onHeartbeat  HeartbeatHandler
	onResult     ResultHandler
	onInventory  InventoryHandler
}

func NewHub() *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
		byMachine: make(map[string]*client),
		inventory: make(map[string]runnerprotocol.MCPInventory),
	}
}

func (h *Hub) SetHandlers(onConnect ConnectHandler, onDisconnect DisconnectHandler, onHeartbeat HeartbeatHandler, onResult ResultHandler) {
	h.handlerMu.Lock()
	h.onConnect = onConnect
	h.onDisconnect = onDisconnect
	h.onHeartbeat = onHeartbeat
	h.onResult = onResult
	h.handlerMu.Unlock()
}

func (h *Hub) SetInventoryHandler(onInventory InventoryHandler) {
	h.handlerMu.Lock()
	h.onInventory = onInventory
	h.handlerMu.Unlock()
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request, identity Identity) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{
		hub:      h,
		conn:     conn,
		identity: identity,
		send:     make(chan outboundFrame, 32),
		done:     make(chan struct{}),
	}

	h.mu.Lock()
	previous := h.byMachine[identity.MachineID]
	h.byMachine[identity.MachineID] = c
	h.mu.Unlock()
	if previous != nil {
		previous.stop()
	}

	h.handlerMu.RLock()
	onConnect := h.onConnect
	h.handlerMu.RUnlock()
	if onConnect != nil {
		go onConnect(r.Context(), identity)
	}

	go c.writePump()
	c.readPump(r.Context())
}

func (h *Hub) Send(machineID string, frame []byte) bool {
	h.mu.RLock()
	c := h.byMachine[machineID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case <-c.done:
		return false
	case c.send <- outboundFrame{payload: append([]byte(nil), frame...)}:
		return true
	default:
		return false
	}
}

// SendAndClose queues one final control frame and closes the socket only after
// the write succeeds. Runner uses this for an operator-requested shutdown so
// the client can distinguish it from a transient network failure and exit
// instead of reconnecting.
func (h *Hub) SendAndClose(machineID string, frame []byte) bool {
	h.mu.RLock()
	c := h.byMachine[machineID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case <-c.done:
		return false
	case c.send <- outboundFrame{payload: append([]byte(nil), frame...), closeAfter: true}:
		return true
	default:
		return false
	}
}

func (h *Hub) Close(machineID string) bool {
	h.mu.RLock()
	c := h.byMachine[machineID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		c.stop()
		return true
	}
}

func (h *Hub) Connected(machineID string) bool {
	h.mu.RLock()
	c := h.byMachine[machineID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *client) readPump(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		c.hub.mu.Lock()
		if c.hub.byMachine[c.identity.MachineID] == c {
			delete(c.hub.byMachine, c.identity.MachineID)
			delete(c.hub.inventory, c.identity.MachineID)
		}
		c.hub.mu.Unlock()
		c.stop()
		c.hub.handlerMu.RLock()
		onDisconnect := c.hub.onDisconnect
		c.hub.handlerMu.RUnlock()
		if onDisconnect != nil {
			onDisconnect(c.identity)
		}
	}()

	c.conn.SetReadLimit(maxMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var envelope runnerprotocol.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		c.hub.handlerMu.RLock()
		onHeartbeat := c.hub.onHeartbeat
		onResult := c.hub.onResult
		c.hub.handlerMu.RUnlock()
		switch envelope.Type {
		case runnerprotocol.MessageHeartbeat:
			var heartbeat runnerprotocol.Heartbeat
			if json.Unmarshal(raw, &heartbeat) == nil && onHeartbeat != nil {
				onHeartbeat(ctx, c.identity, heartbeat)
			}
		case runnerprotocol.MessageResult:
			var result runnerprotocol.Result
			if json.Unmarshal(raw, &result) == nil && onResult != nil {
				onResult(ctx, c.identity, result)
			}
		case runnerprotocol.MessageInventory:
			var inventory runnerprotocol.MCPInventory
			if json.Unmarshal(raw, &inventory) == nil && c.hub.storeMCPInventory(c.identity.MachineID, inventory) {
				c.hub.handlerMu.RLock()
				onInventory := c.hub.onInventory
				c.hub.handlerMu.RUnlock()
				if onInventory != nil {
					onInventory(ctx, c.identity, inventory)
				}
			}
		}
	}
}

func (h *Hub) storeMCPInventory(machineID string, inventory runnerprotocol.MCPInventory) bool {
	if strings.TrimSpace(inventory.Revision) == "" || len(inventory.Servers) > 256 || len(inventory.Config) > 1<<20 || (len(inventory.Config) > 0 && !json.Valid(inventory.Config)) {
		return false
	}
	seen := make(map[string]struct{}, len(inventory.Servers))
	for _, server := range inventory.Servers {
		if strings.TrimSpace(server.Name) == "" || strings.TrimSpace(server.Fingerprint) == "" {
			return false
		}
		if _, exists := seen[server.Name]; exists {
			return false
		}
		seen[server.Name] = struct{}{}
	}
	copyInventory := inventory
	copyInventory.Servers = append([]runnerprotocol.MCPServerSummary(nil), inventory.Servers...)
	copyInventory.Config = append([]byte(nil), inventory.Config...)
	h.mu.Lock()
	h.inventory[machineID] = copyInventory
	h.mu.Unlock()
	return true
}

func (h *Hub) MCPInventory(machineID string) (runnerprotocol.MCPInventory, bool) {
	h.mu.RLock()
	inventory, ok := h.inventory[machineID]
	h.mu.RUnlock()
	if !ok {
		return runnerprotocol.MCPInventory{}, false
	}
	inventory.Servers = append([]runnerprotocol.MCPServerSummary(nil), inventory.Servers...)
	inventory.Config = append([]byte(nil), inventory.Config...)
	return inventory, true
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer c.stop()
	for {
		select {
		case frame := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, frame.payload); err != nil {
				return
			}
			if frame.closeAfter {
				_ = c.conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Runner disconnected"),
					time.Now().Add(writeWait),
				)
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func LogDroppedResult(identity Identity, result runnerprotocol.Result, err error) {
	slog.Warn("runner websocket result rejected", "machine_id", identity.MachineID, "call_id", result.CallID, "error", err)
}
