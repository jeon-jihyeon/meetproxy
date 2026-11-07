package listen_test

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/jeon-jihyeon/meetproxy/app/listen/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDurableRedelivery(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		loss bool
		want error
	}{
		{"cancellation after commit", false, context.Canceled},
		{"connection loss before ACK write", true, listen.ErrReceipt},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			life, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			committed, disconnected := make(chan struct{}), make(chan struct{})
			firstFinished := make(chan struct{})
			acked := make(chan string, 2)
			server := eventServer(t, func(n int32, conn net.Conn, rw *bufio.ReadWriter) {
				id := "envelope-1"
				if n > 1 {
					id = "envelope-2"
				}
				if sendCallback(rw, id, callback(inner)) != nil {
					return
				}
				if n == 1 {
					defer close(firstFinished)
					select {
					case <-committed:
					case <-life.Done():
						return
					}
					if tc.loss {
						_ = conn.Close()
						close(disconnected)
						return
					}
				}
				id, err := readReceipt(rw)
				if err == nil {
					acked <- id
				}
			})
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			token := filepath.Join(dir, "app-token")
			require.NoError(t, os.WriteFile(token, []byte("xapp-fixture"), 0o600))
			config := listen.SocketConfig{Directory: filepath.Join(dir, "runtime"), TokenFile: token,
				BotUser: "U1", APIBase: server.URL, Failures: 1}
			path := filepath.Join(dir, "receipt.json")
			var receipts []listen.Receipt
			var events []listen.Event
			var accepts, writes int
			ackCalls := [2]int{}
			for attempt := range 2 {
				ctx, cancel := context.WithCancel(life)
				source, err := listen.NewSocket(config)
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, source.Close()) })
				ctrl := gomock.NewController(t)
				observed, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
				observed.EXPECT().Next(gomock.Any()).DoAndReturn(func(ctx context.Context) (listen.Delivery, error) {
					delivery, err := source.Next(ctx)
					if err == nil {
						receipts = append(receipts, delivery.Receipt)
					}
					return delivery, err
				}).AnyTimes()
				observed.EXPECT().Ack(gomock.Any(), gomock.Any()).DoAndReturn(
					func(ctx context.Context, receipt listen.Receipt) error {
						ackCalls[attempt]++
						return source.Ack(ctx, receipt)
					}).AnyTimes()
				observed.EXPECT().Close().DoAndReturn(source.Close)
				sink.EXPECT().Accept(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, event listen.Event) error {
						accepts++
						events = append(events, event)
						body, err := json.Marshal(event)
						if err != nil {
							return err
						}
						if attempt > 0 {
							stored, err := os.ReadFile(path)
							assert.Equal(t, body, stored)
							return err
						}
						if err := storeReceipt(path, body); err != nil {
							return err
						}
						writes++
						close(committed)
						if !tc.loss {
							cancel()
							return nil
						}
						select {
						case <-disconnected:
						case <-life.Done():
							return life.Err()
						}
						require.Eventually(t, func() bool {
							_, err := source.Next(life)
							return errors.Is(err, listen.ErrRetries)
						}, time.Second, time.Millisecond)
						return nil
					})
				collector, err := listen.New(observed, sink)
				require.NoError(t, err)

				err = collector.Run(ctx)
				cancel()

				if attempt == 0 {
					require.ErrorIs(t, err, tc.want)
					select {
					case <-firstFinished:
					case <-life.Done():
						t.Fatal("first delivery did not close")
					}
					assert.Empty(t, acked)
				} else {
					require.ErrorIs(t, err, listen.ErrRetries)
				}
			}
			require.Len(t, receipts, 2)
			assert.NotEqual(t, receipts[0], receipts[1])
			require.Len(t, events, 2)
			assert.Equal(t, events[0], events[1])
			assert.Equal(t, 2, accepts)
			assert.Equal(t, 1, writes)
			assert.Equal(t, 1, ackCalls[1])
			if tc.loss {
				assert.Equal(t, 1, ackCalls[0])
			} else {
				assert.Zero(t, ackCalls[0])
			}
			select {
			case id := <-acked:
				assert.Equal(t, "envelope-2", id)
			default:
				t.Fatal("redelivery was not acknowledged")
			}
			assert.Empty(t, acked)
			stored, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.NotContains(t, string(stored), string(receipts[0]))
			assert.NotContains(t, string(stored), string(receipts[1]))
		})
	}
}

func eventServer(t *testing.T, script func(int32, net.Conn, *bufio.ReadWriter)) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	var connections atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "url": "ws" + strings.TrimPrefix(server.URL, "http") + "/socket",
			})
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		if err != nil || rw.Flush() != nil {
			return
		}
		script(connections.Add(1), conn, rw)
	}))
	t.Cleanup(server.Close)
	return server
}

func sendCallback(rw *bufio.ReadWriter, id string, raw []byte) error {
	encoded, err := json.Marshal(id)
	if err != nil {
		return err
	}
	payload := append([]byte(`{"type":"events_api","envelope_id":`), encoded...)
	payload = append(payload, []byte(`,"payload":`)...)
	payload = append(payload, raw...)
	payload = append(payload, '}')
	header := binary.BigEndian.AppendUint64([]byte{0x81, 127}, uint64(len(payload)))
	_, headerErr := rw.Write(header)
	_, bodyErr := rw.Write(payload)
	return errors.Join(headerErr, bodyErr, rw.Flush())
}

func readReceipt(rw *bufio.ReadWriter) (string, error) {
	var header [2]byte
	if _, err := io.ReadFull(rw, header[:]); err != nil {
		return "", err
	}
	if header[0] != 0x81 || header[1]&0x80 == 0 || header[1]&0x7f >= 126 {
		return "", errors.New("invalid fixture ACK")
	}
	data := make([]byte, 4+int(header[1]&0x7f))
	if _, err := io.ReadFull(rw, data); err != nil {
		return "", err
	}
	for i := range data[4:] {
		data[4+i] ^= data[i%4]
	}
	var receipt struct {
		ID string `json:"envelope_id"`
	}
	err := json.Unmarshal(data[4:], &receipt)
	return receipt.ID, err
}
