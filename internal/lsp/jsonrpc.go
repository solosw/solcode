package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// jsonrpcMessage is a minimal JSON-RPC 2.0 envelope used by LSP.
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonrpcError) Error() string {
	if e == nil {
		return "jsonrpc error"
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// conn is a Content-Length framed JSON-RPC connection over stdio.
type conn struct {
	w      io.Writer
	r      *bufio.Reader
	mu     sync.Mutex // write lock
	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan jsonrpcMessage
	closed    chan struct{}
	closeOnce sync.Once
	readErr   error
}

func newConn(r io.Reader, w io.Writer) *conn {
	c := &conn{
		w:       w,
		r:       bufio.NewReader(r),
		pending: make(map[int64]chan jsonrpcMessage),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
	})
}

func (c *conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramsRaw = b
	}
	idRaw, _ := json.Marshal(id)
	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}

	ch := make(chan jsonrpcMessage, 1)
	c.pendingMu.Lock()
	if c.readErr != nil {
		c.pendingMu.Unlock()
		return nil, c.readErr
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.write(msg); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("lsp connection closed")
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("lsp connection closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *conn) Notify(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		paramsRaw = b
	}
	return c.write(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func (c *conn) write(msg jsonrpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(buf.Bytes())
	return err
}

func (c *conn) readLoop() {
	for {
		select {
		case <-c.closed:
			return
		default:
		}
		msg, err := c.readMessage()
		if err != nil {
			c.pendingMu.Lock()
			c.readErr = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			c.Close()
			return
		}
		if len(msg.ID) > 0 && msg.Method == "" {
			// response
			var id int64
			if err := json.Unmarshal(msg.ID, &id); err != nil {
				continue
			}
			c.pendingMu.Lock()
			ch, ok := c.pending[id]
			if ok {
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- msg
				close(ch)
			}
			continue
		}
		// server request/notification — reply to server requests with null result
		if len(msg.ID) > 0 && msg.Method != "" {
			_ = c.write(jsonrpcMessage{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  json.RawMessage("null"),
			})
		}
	}
}

func (c *conn) readMessage() (jsonrpcMessage, error) {
	var contentLength int
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return jsonrpcMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "content-length:") {
			v := strings.TrimSpace(line[len("Content-Length:"):])
			n, err := strconv.Atoi(v)
			if err != nil {
				return jsonrpcMessage{}, fmt.Errorf("invalid Content-Length: %q", v)
			}
			contentLength = n
		}
	}
	if contentLength <= 0 {
		return jsonrpcMessage{}, fmt.Errorf("missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return jsonrpcMessage{}, err
	}
	var msg jsonrpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return jsonrpcMessage{}, err
	}
	return msg, nil
}
