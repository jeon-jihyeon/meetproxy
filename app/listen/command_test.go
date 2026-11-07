package listen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/jeon-jihyeon/meetproxy/app/listen/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func init() {
	if len(os.Args) < 3 || os.Args[1] != "handoff-fixture" {
		return
	}
	if os.Args[2] == "hold" {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1)
	}
	switch os.Args[2] {
	case "accept":
		var event listen.Event
		if json.Unmarshal(data, &event) != nil || !reflect.DeepEqual(event, commandEvent()) {
			os.Exit(1)
		}
	case "wire":
		if len(os.Args) != 4 || os.Args[3] != "literal $(value) `value`" || os.Getenv("LANG") != "fixture" {
			os.Exit(1)
		}
		if os.Getenv("COMMAND_INHERITED") != "" || os.Getenv("COMMAND_SECRET") != "" {
			os.Exit(1)
		}
	case "mark":
		if len(os.Args) != 4 || os.WriteFile(os.Args[3], data, 0o600) != nil {
			os.Exit(1)
		}
	case "durable":
		if len(os.Args) != 4 || storeReceipt(os.Args[3], data) != nil {
			os.Exit(1)
		}
	case "exit":
		_, _ = io.WriteString(os.Stderr, "sensitive fixture content")
		os.Exit(1)
	case "wait":
		time.Sleep(3 * time.Second)
	case "stdout limit":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 64<<10))
	case "stderr over":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), (64<<10)+1))
	case "combined over":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 40<<10))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 40<<10))
	case "pipe":
		binary, err := os.Executable()
		if err != nil {
			os.Exit(1)
		}
		cmd := exec.Command(binary, "handoff-fixture", "hold")
		cmd.Env = []string{}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
	default:
		os.Exit(1)
	}
	os.Exit(0)
}

func storeReceipt(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func TestCommandPayloadBudget(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	tcs := []struct {
		name      string
		text      string
		collector bool
	}{
		{"direct plain", "x", false},
		{"direct HTML sensitive", "<>&", false},
		{"direct Unicode separators", "\u2028\u2029", false},
		{"Collector plain", "x", true},
		{"Collector HTML sensitive", "<>&", true},
		{"Collector Unicode separators", "\u2028\u2029", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text := strings.Repeat(tc.text, ((1<<20)-2)/len(tc.text))
			text += strings.Repeat("x", ((1<<20)-2)%len(tc.text))
			event := listen.Event{
				Source: strings.Repeat(`"\`, 128), ID: strings.Repeat(`"`, 256), Kind: strings.Repeat(`\`, 128),
				Payload: json.RawMessage(`"` + text + `"`),
			}
			require.Len(t, event.Source, 256)
			require.Len(t, event.ID, 256)
			require.Len(t, event.Kind, 128)
			require.Len(t, event.Payload, 1<<20)
			marker := filepath.Join(t.TempDir(), "received.json")
			sink, err := listen.NewCommand(listen.CommandConfig{
				Binary: binary, Arguments: []string{"handoff-fixture", "durable", marker},
			})
			require.NoError(t, err)
			if tc.collector {
				source := mocks.NewMockSubscription(gomock.NewController(t))
				gomock.InOrder(
					source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{Event: event, Receipt: "receipt"}, nil),
					source.EXPECT().Ack(gomock.Any(), listen.Receipt("receipt")).DoAndReturn(
						func(context.Context, listen.Receipt) error {
							assert.FileExists(t, marker)
							return nil
						}),
					source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{}, io.EOF),
					source.EXPECT().Close().Return(nil),
				)
				collector, err := listen.New(source, sink)
				require.NoError(t, err)
				require.NoError(t, collector.Run(context.Background()))
			} else {
				require.NoError(t, sink.Accept(context.Background(), event))
			}

			body, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(body), 4<<20)
			assert.NotEqual(t, byte('\n'), body[len(body)-1])
			var received listen.Event
			require.NoError(t, json.Unmarshal(body, &received))
			assert.Equal(t, event.Source, received.Source)
			assert.Equal(t, event.ID, received.ID)
			assert.Equal(t, event.Kind, received.Kind)
			assert.JSONEq(t, string(event.Payload), string(received.Payload))
		})
	}
}

func TestCommandAccept(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type args struct {
		mode     string
		timeout  time.Duration
		parent   time.Duration
		missing  bool
		canceled bool
	}
	type want struct {
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{name: "durable receipt", args: args{mode: "accept"}},
		{name: "output at limit", args: args{mode: "stdout limit"}},
		{name: "refused or unknown", args: args{mode: "exit"}, want: want{err: listen.ErrSink}},
		{name: "stderr over limit", args: args{mode: "stderr over"}, want: want{err: listen.ErrSink}},
		{name: "combined output limit", args: args{mode: "combined over"}, want: want{err: listen.ErrSink}},
		{name: "start failure", args: args{mode: "accept", missing: true}, want: want{err: listen.ErrSink}},
		{name: "configured timeout", args: args{mode: "wait", timeout: 40 * time.Millisecond},
			want: want{err: listen.ErrSink}},
		{name: "parent deadline", args: args{mode: "wait", timeout: 3 * time.Second, parent: 80 * time.Millisecond},
			want: want{err: listen.ErrSink}},
		{name: "pipe still held", args: args{mode: "pipe"}, want: want{err: listen.ErrSink}},
		{name: "canceled before start", args: args{mode: "mark", canceled: true}, want: want{err: listen.ErrSink}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := filepath.Join(t.TempDir(), "received.json")
			path := binary
			if tc.args.missing {
				path = filepath.Join(t.TempDir(), "missing")
			}
			sink, err := listen.NewCommand(listen.CommandConfig{
				Binary: path, Arguments: []string{"handoff-fixture", tc.args.mode, marker}, Timeout: tc.args.timeout,
			})
			require.NoError(t, err)
			event := commandEvent()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.args.canceled {
				cancel()
			}
			if tc.args.parent != 0 {
				var end context.CancelFunc
				ctx, end = context.WithTimeout(ctx, tc.args.parent)
				defer end()
			}
			started := time.Now()
			err = sink.Accept(ctx, event)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, fmt.Sprint(tc.want.err), fmt.Sprint(err))
			assert.Less(t, time.Since(started), 2*time.Second)
			assert.NoFileExists(t, marker)
		})
	}
}

func TestCommandInvalidEvent(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type args struct {
		source, id, kind string
		payload          json.RawMessage
	}
	tcs := []struct {
		name string
		args args
	}{
		{"empty source", args{"", "1", "changed", json.RawMessage(`{}`)}},
		{"blank source", args{" ", "1", "changed", json.RawMessage(`{}`)}},
		{"long source", args{strings.Repeat("x", 257), "1", "changed", json.RawMessage(`{}`)}},
		{"control source", args{"acme\x00", "1", "changed", json.RawMessage(`{}`)}},
		{"Unicode control source", args{"acme\u0085", "1", "changed", json.RawMessage(`{}`)}},
		{"UTF8 source", args{"acme\xff", "1", "changed", json.RawMessage(`{}`)}},
		{"empty ID", args{"acme", "", "changed", json.RawMessage(`{}`)}},
		{"blank ID", args{"acme", " ", "changed", json.RawMessage(`{}`)}},
		{"long ID", args{"acme", strings.Repeat("x", 257), "changed", json.RawMessage(`{}`)}},
		{"control ID", args{"acme", "1\n", "changed", json.RawMessage(`{}`)}},
		{"Unicode control ID", args{"acme", "1\u0085", "changed", json.RawMessage(`{}`)}},
		{"UTF8 ID", args{"acme", "1\xff", "changed", json.RawMessage(`{}`)}},
		{"empty kind", args{"acme", "1", "", json.RawMessage(`{}`)}},
		{"blank kind", args{"acme", "1", " ", json.RawMessage(`{}`)}},
		{"long kind", args{"acme", "1", strings.Repeat("x", 129), json.RawMessage(`{}`)}},
		{"control kind", args{"acme", "1", "changed\x7f", json.RawMessage(`{}`)}},
		{"Unicode control kind", args{"acme", "1", "changed\u0085", json.RawMessage(`{}`)}},
		{"UTF8 kind", args{"acme", "1", "changed\xff", json.RawMessage(`{}`)}},
		{"nil payload", args{"acme", "1", "changed", nil}},
		{"empty payload", args{"acme", "1", "changed", json.RawMessage{}}},
		{"invalid JSON", args{"acme", "1", "changed", json.RawMessage(`broken`)}},
		{"invalid UTF8", args{"acme", "1", "changed", json.RawMessage{'"', 255, '"'}}},
		{"oversized payload", args{"acme", "1", "changed",
			json.RawMessage(`"` + strings.Repeat("x", (1<<20)-1) + `"`)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := filepath.Join(t.TempDir(), "received.json")
			sink, err := listen.NewCommand(listen.CommandConfig{
				Binary: binary, Arguments: []string{"handoff-fixture", "mark", marker},
			})
			require.NoError(t, err)

			err = sink.Accept(context.Background(), listen.Event{
				Source: tc.args.source, ID: tc.args.id, Kind: tc.args.kind, Payload: tc.args.payload,
			})

			assert.ErrorIs(t, err, listen.ErrEvent)
			assert.Equal(t, listen.ErrEvent.Error(), fmt.Sprint(err))
			assert.NoFileExists(t, marker)
		})
	}
}

func TestCommandWire(t *testing.T) {
	t.Setenv("COMMAND_INHERITED", "synthetic")
	binary, err := os.Executable()
	require.NoError(t, err)
	args := []string{"handoff-fixture", "wire", "literal $(value) `value`"}
	env := []string{"LANG=fixture", "COMMAND_SECRET=synthetic"}
	sink, err := listen.NewCommand(listen.CommandConfig{Binary: binary, Arguments: args, Environment: env})
	require.NoError(t, err)
	args[1], env[0] = "exit", "LANG=changed"
	require.NoError(t, sink.Accept(context.Background(), commandEvent()))
}

func TestCommandConfiguration(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	tcs := []struct {
		name   string
		config listen.CommandConfig
	}{
		{name: "empty"},
		{name: "relative", config: listen.CommandConfig{Binary: "consumer"}},
		{name: "binary NUL", config: listen.CommandConfig{Binary: binary + "\x00"}},
		{name: "argument NUL", config: listen.CommandConfig{Binary: binary, Arguments: []string{"\x00"}}},
		{name: "negative timeout", config: listen.CommandConfig{Binary: binary, Timeout: -time.Second}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink, err := listen.NewCommand(tc.config)
			assert.ErrorIs(t, err, listen.ErrConfig)
			assert.Nil(t, sink)
		})
	}
}

func commandEvent() listen.Event {
	return listen.Event{
		Source: "slack:A1:T1", ID: "Ev1", Kind: "mention",
		Payload: json.RawMessage(`{"type":"app_mention","user":"U1","channel":"C1","ts":"1700000000.000001"}`),
	}
}
