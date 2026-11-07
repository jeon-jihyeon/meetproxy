package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/jeon-jihyeon/meetproxy/app/control/mocks"
	"github.com/jeon-jihyeon/meetproxy/internal/opstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOutputBudgetRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local file journals require a Unix platform")
	}
	tcs := []struct {
		name string
		text string
	}{
		{name: "plain", text: "x"},
		{name: "HTML", text: "<>&"},
		{name: "Unicode separators", text: "\u2028\u2029"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			const limit = 16 << 20
			text := strings.Repeat(tc.text, (limit-11)/len(tc.text)) + strings.Repeat("x", (limit-11)%len(tc.text))
			data := json.RawMessage(`{"text":"` + text + `"}`)
			require.Len(t, data, limit)
			require.True(t, json.Valid(data))
			request := control.Request{
				ID: strings.Repeat("i", 128), Session: "s1", Tool: strings.Repeat("t", 80),
				Arguments: json.RawMessage(`{}`),
			}
			driver := mocks.NewMockDriver(gomock.NewController(t))
			driver.EXPECT().Call(gomock.Any(), request.Tool, gomock.Any()).Return([]byte(data), true, nil).Times(1)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			store := opstore.New(dir)
			first, err := control.New(driver, store).Execute(context.Background(), request)
			require.NoError(t, err)
			require.True(t, first.Recorded)
			assert.Equal(t, "completed", first.State)
			assert.Equal(t, data, first.Output)
			reopened := control.New(driver, opstore.New(dir))
			second, err := reopened.Execute(context.Background(), request)
			require.NoError(t, err)
			found, exists, err := reopened.Lookup(context.Background(), request.ID)
			require.NoError(t, err)
			assert.True(t, exists)
			assert.Equal(t, first, second)
			assert.Equal(t, first, found)
		})
	}
}

func TestRecovery(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("local file journals require a Unix platform")
	}
	tcs := []struct {
		name string
		err  error
		want string
	}{
		{name: "stored completion", want: "completed"},
		{name: "response lost", err: errors.New("transport lost"), want: "unknown"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			driver := mocks.NewMockDriver(ctrl)
			driver.EXPECT().Call(gomock.Any(), "click", gomock.Any()).Return([]byte(`{}`), true, tc.err).Times(1)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			store := opstore.New(dir)
			request := control.Request{ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`)}
			first, err := control.New(driver, store).Execute(ctx, request)
			require.ErrorIs(t, err, tc.err)
			second, err := control.New(nil, store).Execute(ctx, request)
			require.NoError(t, err)
			found, exists, err := control.New(driver, store).Lookup(ctx, "op1")
			require.NoError(t, err)
			assert.True(t, exists)
			assert.Equal(t, tc.want, first.State)
			assert.Equal(t, first, second)
			assert.Equal(t, first, found)
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		init func(context.CancelFunc, *mocks.MockJournal)
		want string
	}{
		{
			name: "before intent", want: "unknown",
			init: func(cancel context.CancelFunc, _ *mocks.MockJournal) { cancel() },
		},
		{
			name: "after intent", want: "not_started",
			init: func(cancel context.CancelFunc, journal *mocks.MockJournal) {
				journal.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).
					DoAndReturn(func(context.Context, string, string, []byte) ([]byte, bool, error) {
						cancel()
						return nil, true, nil
					})
				journal.EXPECT().Save(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(context.Canceled)
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctrl := gomock.NewController(t)
			driver, journal := mocks.NewMockDriver(ctrl), mocks.NewMockJournal(ctrl)
			tc.init(cancel, journal)
			result, err := control.New(driver, journal).Execute(ctx, control.Request{
				ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`),
			})
			assert.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, tc.want, result.State)
			assert.False(t, result.Recorded)
		})
	}
}

func TestCancellationAfterDispatchRecovery(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("local file journals require a Unix platform")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	store := opstore.New(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver := mocks.NewMockDriver(gomock.NewController(t))
	driver.EXPECT().Call(gomock.Any(), "click", gomock.Any()).
		DoAndReturn(func(context.Context, string, []byte) ([]byte, bool, error) {
			cancel()
			return nil, true, context.Canceled
		}).Times(1)
	request := control.Request{ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`)}
	first, err := control.New(driver, store).Execute(ctx, request)
	require.ErrorIs(t, err, context.Canceled)
	second, err := control.New(driver, store).Execute(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "unknown", first.State)
	assert.False(t, first.Recorded)
	assert.Equal(t, first, second)
}
