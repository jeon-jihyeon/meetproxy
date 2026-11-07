package socket

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"
)

const (
	maxQueued      = 8
	maxQueuedBytes = 16 << 20
)

var (
	ErrPending = errors.New("a delivery is awaiting acknowledgement")
	ErrReceipt = errors.New("the delivery receipt is not valid on this connection")
)

type Delivery struct {
	Payload []byte
	Receipt string
}

func (c *Client) Next(ctx context.Context) (Delivery, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return Delivery{}, ErrClosed
	}
	if !c.next.TryLock() {
		return Delivery{}, ErrPending
	}
	defer c.next.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = c.Close()
			return Delivery{}, err
		}
		c.mu.Lock()
		delivery, changed, err := c.take()
		c.mu.Unlock()
		if err != nil || delivery.Receipt != "" {
			return delivery, err
		}
		select {
		case <-ctx.Done():
		case <-changed:
		}
	}
}

func (c *Client) take() (Delivery, <-chan struct{}, error) {
	if c.closed {
		return Delivery{}, nil, ErrClosed
	}
	if c.terminal != nil {
		return Delivery{}, nil, c.terminal
	}
	if c.receipt != "" {
		return Delivery{}, nil, ErrPending
	}
	if !c.started {
		if c.MaxFailures < 1 || c.MinBackoff <= 0 || c.MaxBackoff < c.MinBackoff {
			return Delivery{}, nil, errConnection
		}
		c.started, c.delay = true, c.MinBackoff
		go c.receive()
	}
	if len(c.queue) == 0 {
		return Delivery{}, c.changed, nil
	}
	c.pending = c.queue[0]
	c.queue[0] = envelope{}
	c.queue = c.queue[1:]
	c.receipt = rand.Text()
	return Delivery{Payload: c.pending.Payload, Receipt: c.receipt}, nil, nil
}

func (c *Client) Ack(ctx context.Context, receipt string) error {
	if err := ctx.Err(); err != nil {
		_ = c.Close()
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if receipt == "" || c.conn == nil {
		return ErrReceipt
	}
	if receipt == c.lastReceipt {
		return nil
	}
	if receipt != c.receipt {
		return ErrReceipt
	}
	if err := c.writeACK(c.conn, c.pending.ID); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.queueBytes -= c.pending.size
	c.lastReceipt, c.receipt, c.pending = receipt, "", envelope{}
	return nil
}

func (c *Client) Close() error {
	c.cancel()
	c.mu.Lock()
	c.closed = true
	c.clear()
	started := c.started
	c.mu.Unlock()
	if started {
		<-c.done
	}
	return nil
}

func (c *Client) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Client) clear() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn, c.queue, c.pending = nil, nil, envelope{}
	c.queueBytes, c.receipt, c.lastReceipt = 0, "", ""
	c.signal()
}

func (c *Client) writeACK(conn *Conn, id string) error {
	ack, _ := json.Marshal(map[string]string{"envelope_id": id})
	if c.life.Err() != nil || conn.WriteText(ack) != nil {
		c.clear()
		return errConnection
	}
	c.failures, c.delay = 0, c.MinBackoff
	return nil
}

func (c *Client) receive() {
	defer close(c.done)
	for c.life.Err() == nil {
		err := c.session()
		if !c.retry(err) {
			return
		}
	}
}

func (c *Client) retry(err error) bool {
	c.mu.Lock()
	c.clear()
	if c.life.Err() != nil {
		c.mu.Unlock()
		return false
	}
	if errors.Is(err, ErrAuth) || errors.Is(err, ErrRate) {
		c.terminal = err
		c.mu.Unlock()
		return false
	}
	if errors.Is(err, errRefresh) {
		c.mu.Unlock()
		return true
	}
	c.failures++
	if c.failures >= c.MaxFailures {
		c.terminal = ErrRetries
		c.mu.Unlock()
		return false
	}
	delay := c.delay
	c.delay = min(c.delay*2, c.MaxBackoff)
	c.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.life.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) session() error {
	link, err := c.open(c.life)
	if err != nil {
		return err
	}
	conn, err := Dial(c.life, link, nil, dialTimeout)
	if err != nil {
		return errConnection
	}
	conn.readTimeout = 2 * time.Minute
	stop := context.AfterFunc(c.life, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.conn = conn
	c.mu.Unlock()
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		c.keepalive(conn, done)
	}()
	defer func() {
		_ = conn.Close()
		close(done)
		<-finished
	}()
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			return errConnection
		}
		if err := c.enqueue(conn, raw); err != nil {
			return err
		}
	}
}

func (c *Client) enqueue(conn *Conn, raw []byte) error {
	var in envelope
	if json.Unmarshal(raw, &in) != nil {
		return errConnection
	}
	in.size = len(raw)
	if in.Type == "disconnect" {
		return disconnectError(in.Reason)
	}
	if in.ID == "" {
		if in.Type == "events_api" {
			return errConnection
		}
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != conn {
		return errConnection
	}
	if in.Type != "events_api" {
		return c.writeACK(conn, in.ID)
	}
	count := len(c.queue)
	if c.receipt != "" {
		count++
	}
	if count >= maxQueued || in.size > maxQueuedBytes-c.queueBytes {
		return errConnection
	}
	c.queue = append(c.queue, in)
	c.queueBytes += in.size
	c.signal()
	return nil
}
