package listen

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/slackevent"
	"github.com/jeon-jihyeon/meetproxy/internal/socket"
)

var (
	ErrBusy     = socket.ErrBusy
	ErrSecurity = socket.ErrSecurity
	ErrAuth     = socket.ErrAuth
	ErrRate     = socket.ErrRate
	ErrRetries  = socket.ErrRetries
	ErrPending  = socket.ErrPending
	ErrReceipt  = socket.ErrReceipt
	ErrClosed   = socket.ErrClosed
)

type SocketConfig struct {
	Directory string
	TokenFile string
	APIBase   string
	BotUser   string
	Failures  int
}

type SocketSource struct {
	config   SocketConfig
	mu       sync.Mutex
	client   *socket.Client
	lock     io.Closer
	closed   bool
	reading  bool
	cancel   context.CancelFunc
	closeErr error
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRate.Error() }

func (e *RateLimitError) Unwrap() error { return ErrRate }

func NewSocket(config SocketConfig) (*SocketSource, error) {
	if !filepath.IsAbs(config.Directory) || !filepath.IsAbs(config.TokenFile) ||
		config.Failures < 0 || config.Failures > 100 || !slackevent.ValidUser(config.BotUser) {
		return nil, ErrConfig
	}
	return &SocketSource{config: config}, nil
}

func (s *SocketSource) Next(ctx context.Context) (Delivery, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Delivery{}, ErrClosed
	}
	if s.reading {
		s.mu.Unlock()
		return Delivery{}, ErrPending
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return Delivery{}, err
	}
	if err := s.open(); err != nil {
		s.mu.Unlock()
		return Delivery{}, err
	}
	read, cancel := context.WithCancel(ctx)
	s.cancel, s.reading = cancel, true
	client := s.client
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.cancel, s.reading = nil, false
		s.mu.Unlock()
	}()
	return s.next(read, client)
}

func (s *SocketSource) next(ctx context.Context, client *socket.Client) (Delivery, error) {
	for {
		in, err := client.Next(ctx)
		if err != nil {
			var rate *socket.RateLimitError
			if errors.As(err, &rate) {
				return Delivery{}, &RateLimitError{RetryAfter: rate.RetryAfter}
			}
			return Delivery{}, err
		}
		event, accepted, err := decode(in.Payload, s.config.BotUser)
		if err != nil {
			return Delivery{}, errors.Join(err, s.Close())
		}
		if accepted {
			return Delivery{Event: event, Receipt: Receipt(in.Receipt)}, nil
		}
		if err := client.Ack(ctx, in.Receipt); err != nil {
			return Delivery{}, err
		}
	}
}

func (s *SocketSource) open() error {
	if s.client != nil {
		return nil
	}
	lock, err := socket.Acquire(s.config.Directory)
	if err != nil {
		return err
	}
	token, err := socket.ReadToken(s.config.TokenFile)
	if err != nil {
		return errors.Join(err, closeLock(lock))
	}
	client, err := socket.New(s.config.APIBase, token)
	if err != nil {
		return errors.Join(err, closeLock(lock))
	}
	if s.config.Failures != 0 {
		client.MaxFailures = s.config.Failures
	}
	s.client, s.lock = client, lock
	return nil
}

func (s *SocketSource) Ack(ctx context.Context, receipt Receipt) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return ErrReceipt
	}
	return client.Ack(ctx, string(receipt))
}

func (s *SocketSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.client != nil {
		s.closeErr = s.client.Close()
	}
	if s.lock != nil {
		s.closeErr = errors.Join(s.closeErr, closeLock(s.lock))
	}
	return s.closeErr
}

func closeLock(lock io.Closer) error {
	if lock.Close() != nil {
		return ErrSecurity
	}
	return nil
}
