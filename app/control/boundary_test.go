package control_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/jeon-jihyeon/meetproxy/app/control/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMissingDriver(t *testing.T) {
	t.Parallel()
	result, err := control.New(nil, nil).Execute(context.Background(), control.Request{
		Session: "s1", Tool: "list_apps", Arguments: json.RawMessage(`{}`),
	})
	assert.ErrorIs(t, err, control.ErrDriver)
	assert.Equal(t, "not_started", result.State)
}

func TestUnavailableDriver(t *testing.T) {
	t.Parallel()
	read := control.Request{Session: "s1", Tool: "list_apps", Arguments: json.RawMessage(`{}`)}
	effect := control.Request{ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`)}
	record := func(journal *mocks.MockJournal) {
		gomock.InOrder(
			journal.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(),
				[]byte(`{"id":"op1","tool":"click","state":"unknown","recorded":false}`)).
				Return(nil, true, nil),
			journal.EXPECT().Save(gomock.Any(), "op1", gomock.Any(),
				[]byte(`{"id":"op1","tool":"click","state":"not_started","recorded":true}`)).Return(nil),
		)
	}
	type args struct {
		driver  control.Driver
		request control.Request
	}
	tcs := []struct {
		name     string
		args     args
		init     func(*mocks.MockJournal)
		recorded bool
	}{
		{"nil read", args{nil, read}, func(*mocks.MockJournal) {}, false},
		{"typed nil read", args{(*mocks.MockDriver)(nil), read}, func(*mocks.MockJournal) {}, false},
		{"nil effect", args{nil, effect}, record, true},
		{"typed nil effect", args{(*mocks.MockDriver)(nil), effect}, record, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			journal := mocks.NewMockJournal(gomock.NewController(t))
			tc.init(journal)
			client := control.New(tc.args.driver, journal)

			result, err := client.Execute(context.Background(), tc.args.request)

			require.ErrorIs(t, err, control.ErrDriver)
			assert.Equal(t, control.Result{
				ID: tc.args.request.ID, Tool: tc.args.request.Tool, State: "not_started", Recorded: tc.recorded,
			}, result)
		})
	}
}

func TestUnavailableJournal(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name    string
		journal control.Journal
	}{
		{"nil", nil},
		{"typed nil", (*mocks.MockJournal)(nil)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := mocks.NewMockDriver(gomock.NewController(t))
			driver.EXPECT().Call(gomock.Any(), "list_apps", []byte(`{"session":"s1"}`)).
				Return([]byte(`[]`), true, nil).Times(1)
			client := control.New(driver, tc.journal)

			read, err := client.Execute(context.Background(), control.Request{
				Session: "s1", Tool: "list_apps", Arguments: json.RawMessage(`{}`),
			})
			require.NoError(t, err)
			assert.Equal(t, control.Result{Tool: "list_apps", State: "completed", Output: json.RawMessage(`[]`)}, read)

			effect, err := client.Execute(context.Background(), control.Request{
				ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`),
			})
			require.ErrorIs(t, err, control.ErrJournal)
			assert.Equal(t, control.Result{ID: "op1", Tool: "click", State: "not_started"}, effect)
		})
	}
}

func TestArgumentsLimit(t *testing.T) {
	t.Parallel()
	type args struct{ id, tool string }
	tcs := []struct {
		name string
		args args
	}{
		{name: "read", args: args{tool: "list_apps"}},
		{name: "effect", args: args{id: "op1", tool: "click"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			driver, journal := mocks.NewMockDriver(ctrl), mocks.NewMockJournal(ctrl)
			request := control.Request{
				ID: tc.args.id, Session: "s1", Tool: tc.args.tool,
				Arguments: json.RawMessage(`{"text":"` + strings.Repeat("x", 1<<20) + `"}`),
			}
			result, err := control.New(driver, journal).Execute(context.Background(), request)
			assert.ErrorIs(t, err, control.ErrInput)
			assert.Equal(t, "not_started", result.State)
			fingerprint, err := control.Fingerprint(request)
			assert.ErrorIs(t, err, control.ErrInput)
			assert.Empty(t, fingerprint)
		})
	}
}

func TestEffectOutputLimit(t *testing.T) {
	ctrl := gomock.NewController(t)
	driver, journal := mocks.NewMockDriver(ctrl), mocks.NewMockJournal(ctrl)
	journal.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
	driver.EXPECT().Call(gomock.Any(), "click", gomock.Any()).
		Return([]byte(`"`+strings.Repeat("x", (16<<20)-1)+`"`), true, nil).Times(1)
	journal.EXPECT().Save(gomock.Any(), "op1", gomock.Any(),
		[]byte(`{"id":"op1","tool":"click","state":"unknown","recorded":true}`)).Return(nil)
	result, err := control.New(driver, journal).Execute(context.Background(), control.Request{
		ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{}`),
	})
	require.ErrorIs(t, err, control.ErrOutput)
	assert.Equal(t, control.Result{ID: "op1", Tool: "click", State: "unknown", Recorded: true}, result)
}
