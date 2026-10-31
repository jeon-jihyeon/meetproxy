package slackstate_test

import (
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
	"github.com/stretchr/testify/assert"
)

func TestHealth(t *testing.T) {
	t.Parallel()
	type args struct {
		ok      bool
		failure string
		found   int
		paused  bool
	}
	type want struct {
		ok, failed bool
		failure    string
		found      int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"success", args{found: 2}, want{ok: true, found: 2}},
		{"failure", args{failure: "read failed", found: 1}, want{failed: true, failure: "read failed", found: 1}},
		{"pause replaces a run", args{failure: "read failed", found: 2, paused: true},
			want{failure: slackstate.Paused}},
		{"a kept pause", args{failure: slackstate.Paused}, want{failure: slackstate.Paused}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Unix(1893456000, 0).UTC()
			got := slackstate.RestoreHealth(tc.args.ok, tc.args.failure, tc.args.found, now).Collect(tc.args.paused)
			assert.Equal(t, tc.want.ok, got.OK())
			assert.Equal(t, tc.want.failed, got.Failed())
			assert.Equal(t, tc.want.failure, got.Error())
			assert.Equal(t, tc.want.found, got.Found())
			assert.Equal(t, now, got.At())
		})
	}
}

func TestHealthNeedsUpdate(t *testing.T) {
	t.Parallel()
	type args struct {
		ok      bool
		failure string
		found   int
		elapsed time.Duration
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"same recent result", args{ok: true, found: 2, elapsed: 9 * time.Minute}, false},
		{"refresh boundary", args{ok: true, found: 2, elapsed: 10 * time.Minute}, true},
		{"success changed", args{found: 2}, true},
		{"failure changed", args{ok: true, failure: "read failed", found: 2}, true},
		{"count changed", args{ok: true, found: 3}, true},
		{"clock moved back", args{ok: true, found: 2, elapsed: -time.Minute}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Unix(1893456000, 0)
			got := slackstate.RestoreHealth(true, "", 2, now)
			assert.Equal(t, tc.want, got.NeedsUpdate(tc.args.ok, tc.args.failure, tc.args.found, now.Add(tc.args.elapsed)))
		})
	}
}

func TestHealthFailed(t *testing.T) {
	t.Parallel()
	type args struct {
		ok      bool
		failure string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"failure", args{failure: "read failed"}, true},
		{"success", args{ok: true, failure: "read failed"}, false},
		{"no error", args{}, false},
		{"pause", args{failure: slackstate.Paused}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := slackstate.RestoreHealth(tc.args.ok, tc.args.failure, 0, time.Time{})
			assert.Equal(t, tc.want, got.Failed())
		})
	}
}
