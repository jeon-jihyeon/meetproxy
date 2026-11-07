package control_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/jeon-jihyeon/meetproxy/app/control/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestExecute(t *testing.T) {
	t.Parallel()
	fault := errors.New("injected failure")
	read := control.Request{Session: "s1", Tool: "list_apps", Arguments: json.RawMessage(`{}`)}
	write := control.Request{
		ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{"element_token":"e1"}`),
	}
	type args struct {
		request control.Request
		journal bool
	}
	type want struct {
		state    string
		recorded bool
		err      error
	}
	tcs := []struct {
		name string
		args args
		init func(*mocks.MockDriver, *mocks.MockJournal)
		want want
	}{
		{
			name: "read needs no Inbox or journal", args: args{request: read},
			init: func(d *mocks.MockDriver, _ *mocks.MockJournal) {
				d.EXPECT().Call(gomock.Any(), "list_apps", []byte(`{"session":"s1"}`)).Return([]byte(`[]`), true, nil)
			},
			want: want{state: "completed"},
		},
		{
			name: "write requires durable intent", args: args{request: write},
			init: func(_ *mocks.MockDriver, _ *mocks.MockJournal) {},
			want: want{state: "not_started", err: control.ErrJournal},
		},
		{
			name: "intent failure prevents dispatch", args: args{request: write, journal: true},
			init: func(_ *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, false, fault)
			},
			want: want{state: "unknown", err: fault},
		},
		{
			name: "existing intent is never dispatched", args: args{request: write, journal: true},
			init: func(_ *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).
					Return([]byte(`{"id":"op1","tool":"click","state":"unknown","recorded":false}`), false, nil)
			},
			want: want{state: "unknown"},
		},
		{
			name: "corrupt existing receipt remains unknown", args: args{request: write, journal: true},
			init: func(_ *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return([]byte(`broken`), false, nil)
			},
			want: want{state: "unknown", err: control.ErrReceipt},
		},
		{
			name: "receipt operation ID mismatch remains unknown", args: args{request: write, journal: true},
			init: func(_ *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).
					Return([]byte(`{"id":"other","tool":"click","state":"completed","output":{},"recorded":true}`), false, nil)
			},
			want: want{state: "unknown", err: control.ErrReceipt},
		},
		{
			name: "receipt tool mismatch remains unknown", args: args{request: write, journal: true},
			init: func(_ *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).
					Return([]byte(`{"id":"op1","tool":"scroll","state":"completed","output":{},"recorded":true}`), false, nil)
			},
			want: want{state: "unknown", err: control.ErrReceipt},
		},
		{
			name: "invalid driver response remains unknown", args: args{request: write, journal: true},
			init: func(d *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
				d.EXPECT().Call(gomock.Any(), "click", gomock.Any()).Return([]byte(`broken`), true, nil)
				j.EXPECT().Save(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil)
			},
			want: want{state: "unknown", recorded: true, err: control.ErrOutput},
		},
		{
			name: "effect result is stored", args: args{request: write, journal: true},
			init: func(d *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
				d.EXPECT().Call(gomock.Any(), "click", []byte(`{"element_token":"e1","session":"s1"}`)).
					Return([]byte(`{"clicked":true}`), true, nil)
				j.EXPECT().Save(gomock.Any(), "op1", gomock.Any(),
					[]byte(`{"id":"op1","tool":"click","state":"completed","output":{"clicked":true},"recorded":true}`)).Return(nil)
			},
			want: want{state: "completed", recorded: true},
		},
		{
			name: "lost response remains unknown", args: args{request: write, journal: true},
			init: func(d *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
				d.EXPECT().Call(gomock.Any(), "click", gomock.Any()).Return(nil, true, fault)
				j.EXPECT().Save(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil)
			},
			want: want{state: "unknown", recorded: true, err: fault},
		},
		{
			name: "process start failure", args: args{request: write, journal: true},
			init: func(d *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
				d.EXPECT().Call(gomock.Any(), "click", gomock.Any()).Return(nil, false, fault)
				j.EXPECT().Save(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil)
			},
			want: want{state: "not_started", recorded: true, err: fault},
		},
		{
			name: "result storage failure preserves observed success", args: args{request: write, journal: true},
			init: func(d *mocks.MockDriver, j *mocks.MockJournal) {
				j.EXPECT().Claim(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(nil, true, nil)
				d.EXPECT().Call(gomock.Any(), "click", gomock.Any()).Return([]byte(`{}`), true, nil)
				j.EXPECT().Save(gomock.Any(), "op1", gomock.Any(), gomock.Any()).Return(fault)
			},
			want: want{state: "completed", err: fault},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			driver, journal := mocks.NewMockDriver(ctrl), mocks.NewMockJournal(ctrl)
			tc.init(driver, journal)
			var store control.Journal
			if tc.args.journal {
				store = journal
			}
			out, err := control.New(driver, store).Execute(context.Background(), tc.args.request)
			assert.Equal(t, tc.want.state, out.State)
			assert.Equal(t, tc.want.recorded, out.Recorded)
			assert.ErrorIs(t, err, tc.want.err)
		})
	}
}

func TestRequestBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	driver, journal := mocks.NewMockDriver(ctrl), mocks.NewMockJournal(ctrl)
	digest := sha256.Sum256([]byte("click\n" + `{"element_token":"e1","session":"s1"}`))
	fingerprint := hex.EncodeToString(digest[:])
	actual, err := control.Fingerprint(control.Request{
		ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{"element_token":"e1"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, fingerprint, actual)
	journal.EXPECT().Claim(gomock.Any(), "op1", fingerprint, gomock.Any()).Return(nil, true, nil)
	driver.EXPECT().Call(gomock.Any(), "click", []byte(`{"element_token":"e1","session":"s1"}`)).
		Return([]byte(`{}`), true, nil)
	journal.EXPECT().Save(gomock.Any(), "op1", fingerprint, gomock.Any()).Return(nil)
	_, err = control.New(driver, journal).Execute(ctx, control.Request{
		ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(`{"element_token":"e1"}`),
	})
	require.NoError(t, err)
}

func TestInvalidInput(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args json.RawMessage
	}{
		{name: "non object", args: json.RawMessage(`[]`)},
		{name: "null object", args: json.RawMessage(`null`)},
		{name: "trailing value", args: json.RawMessage(`{} {}`)},
		{name: "different session", args: json.RawMessage(`{"session":"s2"}`)},
		{name: "non string session", args: json.RawMessage(`{"session":[]}`)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := control.New(nil, nil).Execute(context.Background(), control.Request{
				Session: "s1", Tool: "list_apps", Arguments: tc.args,
			})
			assert.ErrorIs(t, err, control.ErrInput)
			assert.Equal(t, "not_started", out.State)
		})
	}
}
