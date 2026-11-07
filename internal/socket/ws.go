package socket

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opContinue byte = 0x0
	opText     byte = 0x1
	opBinary   byte = 0x2
	opClose    byte = 0x8
	opPing     byte = 0x9
	opPong     byte = 0xA

	maxMessage   = 16 << 20
	maxControl   = 125
	writeTimeout = 10 * time.Second
)

var ErrClosed = errors.New("websocket closed")

type Conn struct {
	nc          net.Conn
	br          *bufio.Reader
	readTimeout time.Duration
	mu          sync.Mutex
}

func Dial(ctx context.Context, raw string, conf *tls.Config, timeout time.Duration) (*Conn, error) {
	life, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	switch {
	case u.Scheme == "wss" && port == "":
		port = "443"
	case u.Scheme == "ws" && port == "":
		port = "80"
	case u.Scheme != "ws" && u.Scheme != "wss":
		return nil, fmt.Errorf("websocket URL must be ws or wss, not %q", u.Scheme)
	}
	d := net.Dialer{}
	nc, err := d.DialContext(life, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	rawConn := nc
	stopCancel := context.AfterFunc(life, func() { _ = rawConn.Close() })
	defer stopCancel()
	deadline, _ := life.Deadline()
	if err = nc.SetDeadline(deadline); err != nil {
		_ = nc.Close()
		return nil, err
	}
	if u.Scheme == "wss" {
		nc, err = negotiateTLS(life, nc, u.Hostname(), conf)
		if err != nil {
			_ = rawConn.Close()
			return nil, connectionError(life, err)
		}
	}
	conn := &Conn{nc: nc, br: bufio.NewReader(nc)}
	if err := conn.handshake(u); err != nil {
		_ = nc.Close()
		return nil, connectionError(life, err)
	}
	if err = life.Err(); err != nil {
		_ = nc.Close()
		return nil, err
	}
	if err = nc.SetDeadline(time.Time{}); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return conn, nil
}

func negotiateTLS(ctx context.Context, nc net.Conn, host string, conf *tls.Config) (net.Conn, error) {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if conf != nil {
		c = conf.Clone()
	}
	if c.ServerName == "" {
		c.ServerName = host
	}
	tc := tls.Client(nc, c)
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return tc, nil
}

func connectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return err
}

// RFC 6455 requires SHA1 for the handshake digest
func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (c *Conn) handshake(u *url.URL) error {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req := "GET " + u.RequestURI() + " HTTP/1.1\r\nHost: " + u.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(c.nc, req); err != nil {
		return err
	}
	responseReader := bufio.NewReader(io.LimitReader(c.br, 64<<10))
	resp, err := http.ReadResponse(responseReader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode != http.StatusSwitchingProtocols:
		return fmt.Errorf("websocket handshake: HTTP %d", resp.StatusCode)
	case !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !hasToken(resp.Header.Get("Connection"), "upgrade"):
		return errors.New("websocket handshake: no upgrade in the answer")
	case resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key):
		return errors.New("websocket handshake: Sec-WebSocket-Accept does not match the key")
	}
	c.br = bufio.NewReader(io.MultiReader(responseReader, c.br))
	return nil
}

func hasToken(header, token string) bool {
	for t := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(t), token) {
			return true
		}
	}
	return false
}

type frame struct {
	fin     bool
	op      byte
	payload []byte
}

func (c *Conn) readFrame() (frame, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return frame{}, err
	}
	f := frame{fin: h[0]&0x80 != 0, op: h[0] & 0x0f}
	if h[0]&0x70 != 0 {
		return frame{}, errors.New("websocket: reserved bits set")
	}
	if h[1]&0x80 != 0 {
		return frame{}, errors.New("websocket: masked frame from the server")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return frame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return frame{}, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if f.op >= opClose && (!f.fin || n > maxControl) {
		return frame{}, errors.New("websocket: fragmented or oversized control frame")
	}
	if n > maxMessage {
		return frame{}, fmt.Errorf("websocket: frame of %d bytes over the limit", n)
	}
	f.payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, f.payload); err != nil {
		return frame{}, err
	}
	return f, nil
}

func (c *Conn) ReadMessage() ([]byte, error) {
	var msg []byte
	started := false
	for {
		if c.readTimeout > 0 {
			_ = c.nc.SetReadDeadline(time.Now().Add(c.readTimeout))
		}
		f, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch f.op {
		case opPing:
			if err := c.write(opPong, f.payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			code := f.payload[:min(len(f.payload), 2)]
			_ = c.write(opClose, code)
			return nil, fmt.Errorf("%w by the server %x", ErrClosed, code)
		case opText, opBinary:
			if started {
				return nil, errors.New("websocket: new message inside a fragmented one")
			}
			started, msg = true, f.payload
		case opContinue:
			if !started {
				return nil, errors.New("websocket: continuation without a message")
			}
			if len(msg)+len(f.payload) > maxMessage {
				return nil, errors.New("websocket: message over the limit")
			}
			msg = append(msg, f.payload...)
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %d", f.op)
		}
		if f.fin {
			return msg, nil
		}
	}
}

func (c *Conn) WriteText(p []byte) error { return c.write(opText, p) }

func (c *Conn) Ping() error { return c.write(opPing, nil) }

func (c *Conn) Close() error {
	return c.nc.Close()
}

func (c *Conn) write(op byte, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := make([]byte, 0, 14+len(p))
	b = append(b, 0x80|op)
	switch {
	case len(p) < 126:
		b = append(b, 0x80|byte(len(p)))
	case len(p) <= 0xffff:
		b = binary.BigEndian.AppendUint16(append(b, 0x80|126), uint16(len(p)))
	default:
		b = binary.BigEndian.AppendUint64(append(b, 0x80|127), uint64(len(p)))
	}
	var key [4]byte
	_, _ = rand.Read(key[:])
	b = append(b, key[:]...)
	start := len(b)
	b = append(b, p...)
	for i := range b[start:] {
		b[start+i] ^= key[i%4]
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := c.nc.Write(b)
	return err
}
