package socket_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/socket"
	"github.com/stretchr/testify/assert"
)

func TestDialRejectsOversizedUpgradeHeaders(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(sum[:]))
		w.Header().Set("X-Extra", strings.Repeat("x", 64<<10))
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	t.Cleanup(server.Close)
	conn, err := socket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil, time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	assert.Error(t, err)
}
