package control_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/jeon-jihyeon/meetproxy/app/control/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestLookup(t *testing.T) {
	t.Parallel()
	type args struct {
		data    string
		found   bool
		readErr error
	}
	type want struct {
		state  string
		exists bool
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{name: "missing", want: want{state: "unknown"}},
		{name: "read failed", args: args{readErr: context.Canceled},
			want: want{state: "unknown", err: context.Canceled}},
		{name: "corrupt receipt", args: args{found: true, data: `broken`},
			want: want{state: "unknown", exists: true, err: control.ErrReceipt}},
		{name: "invalid state", args: args{found: true,
			data: `{"id":"op1","tool":"click","state":"failed","recorded":true}`},
			want: want{state: "unknown", exists: true, err: control.ErrReceipt}},
		{name: "different identity", args: args{found: true,
			data: `{"id":"other","tool":"click","state":"completed","recorded":true,"output":{}}`},
			want: want{state: "unknown", exists: true, err: control.ErrReceipt}},
		{name: "incomplete identity", args: args{found: true, data: `{"id":"op1","state":"unknown"}`},
			want: want{state: "unknown", exists: true, err: control.ErrReceipt}},
		{name: "unrecorded completion", args: args{found: true,
			data: `{"id":"op1","tool":"click","state":"completed","output":{}}`},
			want: want{state: "unknown", exists: true, err: control.ErrReceipt}},
		{name: "stored result", args: args{found: true,
			data: `{"id":"op1","tool":"click","state":"completed","recorded":true,"output":{}}`},
			want: want{state: "completed", exists: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			journal := mocks.NewMockJournal(gomock.NewController(t))
			journal.EXPECT().Read(gomock.Any(), "op1").Return([]byte(tc.args.data), tc.args.found, tc.args.readErr)
			result, exists, err := control.New(nil, journal).Lookup(context.Background(), "op1")
			assert.Equal(t, tc.want.state, result.State)
			assert.Equal(t, tc.want.exists, exists)
			assert.ErrorIs(t, err, tc.want.err)
		})
	}
}

func TestLookupUnavailableJournal(t *testing.T) {
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
			result, exists, err := control.New(nil, tc.journal).Lookup(context.Background(), "op1")
			assert.ErrorIs(t, err, control.ErrJournal)
			assert.False(t, exists)
			assert.Empty(t, result)
		})
	}
}

func TestOutputLimit(t *testing.T) {
	t.Parallel()
	driver := mocks.NewMockDriver(gomock.NewController(t))
	driver.EXPECT().Call(gomock.Any(), "list_apps", gomock.Any()).
		Return([]byte(`"`+strings.Repeat("x", 16<<20)+`"`), true, nil)
	result, err := control.New(driver, nil).Execute(context.Background(), control.Request{
		Session: "s1", Tool: "list_apps", Arguments: []byte(`{}`),
	})
	assert.ErrorIs(t, err, control.ErrOutput)
	assert.Equal(t, "unknown", result.State)
	assert.Empty(t, result.Output)
}

func TestOpenRejectsTwoJournals(t *testing.T) {
	t.Parallel()
	journal := mocks.NewMockJournal(gomock.NewController(t))
	client, err := control.Open(control.Config{Data: "operations", Journal: journal})
	require.Error(t, err)
	assert.Nil(t, client)
}

func TestOpenOptionalJournal(t *testing.T) {
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
			client, err := control.Open(control.Config{Data: t.TempDir(), Journal: tc.journal})
			require.NoError(t, err)
			assert.NotNil(t, client)
		})
	}
}

func TestFingerprintRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	value, err := control.Fingerprint(control.Request{Session: "s1", Tool: "click", Arguments: []byte(`{}`)})
	assert.ErrorIs(t, err, control.ErrInput)
	assert.Empty(t, value)
}
