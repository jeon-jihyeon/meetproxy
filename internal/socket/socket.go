package socket

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBase = "https://slack.com/api"
	dialTimeout = 10 * time.Second
)

var (
	ErrAuth       = errors.New("slack refused the app token")
	ErrBase       = errors.New("a test API base must be a loopback URL")
	ErrRetries    = errors.New("listen reached its consecutive failure limit")
	ErrRate       = errors.New("slack limited connection attempts")
	errConnection = errors.New("socket mode connection failed")
	errRefresh    = errors.New("slack requested a connection refresh")
)

type envelope struct {
	ID      string          `json:"envelope_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Reason  string          `json:"reason"`
	size    int
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRate.Error() }

func (e *RateLimitError) Unwrap() error { return ErrRate }

type Client struct {
	base        string
	token       string
	local       bool
	http        *http.Client
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	MaxFailures int
	mu          sync.Mutex
	next        sync.Mutex
	life        context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	changed     chan struct{}
	started     bool
	closed      bool
	terminal    error
	conn        *Conn
	queue       []envelope
	queueBytes  int
	pending     envelope
	receipt     string
	lastReceipt string
	failures    int
	delay       time.Duration
}

func New(base, token string) (*Client, error) {
	if !validToken(token) {
		return nil, ErrAuth
	}
	local := base != "" && base != DefaultBase
	if local && !loopbackURL(base, "http", "https") {
		return nil, ErrBase
	}
	if base == "" {
		base = DefaultBase
	}
	life, cancel := context.WithCancel(context.Background())
	return &Client{
		base: strings.TrimSuffix(base, "/"), token: token, local: local,
		http: &http.Client{Timeout: dialTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		MinBackoff: time.Second, MaxBackoff: 30 * time.Second, MaxFailures: 8,
		life: life, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}),
	}, nil
}

func loopbackURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return false
	}
	allowed := false
	for _, scheme := range schemes {
		allowed = allowed || u.Scheme == scheme
	}
	ip := net.ParseIP(u.Hostname())
	return allowed && ip != nil && ip.IsLoopback()
}

func disconnectError(reason string) error {
	switch reason {
	case "warning", "refresh_requested":
		return errRefresh
	case "link_disabled":
		return ErrAuth
	default:
		return errConnection
	}
}

func (c *Client) keepalive(conn *Conn, done <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if conn.Ping() != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *Client) open(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/apps.connections.open", nil)
	if err != nil {
		return "", errConnection
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", errConnection
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		seconds, parseErr := strconv.ParseUint(strings.TrimSpace(resp.Header.Get("Retry-After")), 10, 31)
		if parseErr != nil {
			seconds = 0
		}
		return "", &RateLimitError{RetryAfter: time.Duration(seconds) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		return "", errConnection
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return "", errConnection
	}
	return c.connection(body)
}

func (c *Client) connection(body []byte) (string, error) {
	var out struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &out) != nil {
		return "", errConnection
	}
	if !out.OK {
		switch out.Error {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "not_allowed_token_type", "missing_scope":
			return "", ErrAuth
		default:
			return "", errConnection
		}
	}
	u, err := url.Parse(out.URL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Hostname() == "" ||
		(c.local && !loopbackURL(out.URL, "ws", "wss")) || (!c.local && u.Scheme != "wss") {
		return "", errConnection
	}
	return out.URL, nil
}
