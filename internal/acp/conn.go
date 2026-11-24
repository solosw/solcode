package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type Conn struct {
	w         io.Writer
	r         *bufio.Reader
	writeMu   sync.Mutex
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan jsonrpcMessage
	closed    chan struct{}
	closeOnce sync.Once
	readErr   error
}

func NewConn(r io.Reader, w io.Writer) *Conn {
	c := &Conn{
		w:       w,
		r:       bufio.NewReader(r),
		pending: make(map[string]chan jsonrpcMessage),
		closed:  make(chan struct{}),
	}
	return c
}

func (c *Conn) Close() {
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

func (c *Conn) Closed() <-chan struct{} {
	return c.closed
}

func (c *Conn) Read() (jsonrpcMessage, error) {
	select {
	case <-c.closed:
		if c.readErr != nil {
			return jsonrpcMessage{}, c.readErr
		}
		return jsonrpcMessage{}, io.EOF
	default:
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		c.pendingMu.Lock()
		c.readErr = err
		c.pendingMu.Unlock()
		c.Close()
		return jsonrpcMessage{}, err
	}
	if len(line) == 0 {
		return jsonrpcMessage{}, fmt.Errorf("empty jsonrpc line")
	}
	var msg jsonrpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return jsonrpcMessage{}, err
	}
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}
	if isResponse(msg) {
		c.dispatchResponse(msg)
	}
	return msg, nil
}

func (c *Conn) Write(msg jsonrpcMessage) error {
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(append(body, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *Conn) Reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.Write(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	})
}

func (c *Conn) ReplyError(id json.RawMessage, code int, message string) error {
	return c.Write(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: message},
	})
}

func (c *Conn) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.Write(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
	})
}

func (c *Conn) Call(ctx context.Context, method string, params any, result any) error {
	return c.call(ctx, method, params, result, false)
}

func (c *Conn) call(ctx context.Context, method string, params any, result any, notifyCancel bool) error {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	ch := make(chan jsonrpcMessage, 1)
	key := string(idRaw)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()

	if err := c.Write(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  raw,
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		if notifyCancel {
			_ = c.Notify("$/cancel_request", map[string]any{"requestId": id})
		}
		return ctx.Err()
	case <-c.closed:
		if c.readErr != nil {
			return c.readErr
		}
		return io.EOF
	case resp, ok := <-ch:
		if !ok {
			return io.EOF
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		return json.Unmarshal(resp.Result, result)
	}
}

func (c *Conn) dispatchResponse(msg jsonrpcMessage) {
	key := string(msg.ID)
	c.pendingMu.Lock()
	ch, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
	}
	close(ch)
}

func isResponse(msg jsonrpcMessage) bool {
	return len(msg.ID) > 0 && msg.Method == ""
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}
