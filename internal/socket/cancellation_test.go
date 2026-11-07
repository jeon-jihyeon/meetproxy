package socket_test

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/socket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialCancellationDuringUpgrade(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	accepted, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release); _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		close(accepted)
		<-release
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := socket.Dial(ctx, "ws://"+listener.Addr().String(), nil, 5*time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("test connection was not accepted")
	}
	cancel()
	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the WebSocket upgrade")
	}
}
