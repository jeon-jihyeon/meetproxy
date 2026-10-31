package signin_test

import (
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/signin"
	"github.com/stretchr/testify/assert"
)

func TestWindowObserve(t *testing.T) {
	t.Parallel()
	type args struct {
		stage   signin.Stage
		printed string
		page    signin.Observation
	}
	tcs := []struct {
		name string
		args args
		want signin.Stage
	}{
		{"printed page", args{signin.Initial, "printed", signin.Observation{Known: true, Loopback: true,
			Fingerprint: "printed"}}, signin.Initial},
		{"provider", args{stage: signin.Initial, page: signin.Observation{Known: true}}, signin.Away},
		{"callback missed provider", args{signin.Initial, "printed", signin.Observation{Known: true,
			Loopback: true, Fingerprint: "callback"}}, signin.Done},
		{"callback loading", args{stage: signin.Away, page: signin.Observation{Known: true, Loopback: true,
			Loading: true}}, signin.Back},
		{"callback loaded", args{stage: signin.Away, page: signin.Observation{Known: true, Loopback: true}}, signin.Done},
		{"callback still loading", args{stage: signin.Back, page: signin.Observation{Known: true, Loopback: true,
			Loading: true}}, signin.Back},
		{"callback left loopback", args{stage: signin.Back, page: signin.Observation{Known: true, Loading: true}}, signin.Done},
		{"unknown address", args{stage: signin.Back, page: signin.Observation{}}, signin.Back},
		{"unknown saved stage", args{stage: signin.Unknown, page: signin.Observation{Known: true, Loopback: true}}, signin.Unknown},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			window := signin.RestoreWindow(time.Time{}, tc.args.stage, tc.args.printed, "origin", true)
			assert.Equal(t, tc.want, window.Observe(tc.args.page).Stage())
		})
	}
}

func TestWindowOwnership(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	type args struct {
		age              time.Duration
		origin           string
		scripted, single bool
		observed         string
	}
	type want struct{ expired, inspect, owns bool }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"owned", args{time.Minute, "origin", true, true, "origin"}, want{false, true, true}},
		{"give up", args{signin.GiveUp, "origin", true, true, "origin"}, want{true, true, true}},
		{"expired identity", args{signin.Keep, "origin", true, true, "origin"}, want{true, false, true}},
		{"legacy record", args{time.Minute, "", true, true, ""}, want{false, false, false}},
		{"unsupported browser", args{time.Minute, "origin", false, true, "origin"}, want{false, false, true}},
		{"multiple tabs", args{time.Minute, "origin", true, false, "origin"}, want{false, true, false}},
		{"other origin", args{time.Minute, "origin", true, true, "other"}, want{false, true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			window := signin.RestoreWindow(now.Add(-tc.args.age), signin.Initial, "", tc.args.origin, tc.args.scripted)
			assert.Equal(t, tc.want, want{window.Expired(now), window.MayInspect(now),
				window.Owns(tc.args.single, tc.args.observed)})
		})
	}
}

func TestWindowAfterClose(t *testing.T) {
	t.Parallel()
	type args struct {
		gone   bool
		origin string
	}
	type want struct{ open, keep bool }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"closed", args{true, "origin"}, want{}},
		{"retry owned page", args{false, "origin"}, want{true, true}},
		{"open after callback", args{false, "other"}, want{true, false}},
		{"missing origin", args{false, ""}, want{true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			window := signin.RestoreWindow(time.Time{}, signin.Done, "", "origin", true)
			closed := window.AfterClose(tc.args.gone, tc.args.origin)
			assert.Equal(t, tc.want, want{closed.Open(), closed.KeepsRecord()})
		})
	}
}

func TestWindowCloseDirect(t *testing.T) {
	t.Parallel()
	type args struct {
		first  string
		failed bool
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"previous read owns callback", args{"origin", false}, true},
		{"other server", args{"other", false}, false},
		{"failed intervening read", args{"origin", true}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			window := signin.RestoreWindow(time.Time{}, signin.Back, "", "origin", true)
			window = window.Observe(signin.Observation{Known: true, Loopback: true, Loading: true, Origin: tc.args.first})
			steps := []signin.Window{window, window.ReadFailed()}
			index := 0
			if tc.args.failed {
				index = 1
			}
			window = steps[index].Observe(signin.Observation{Known: true})
			assert.Equal(t, signin.Done, window.Stage())
			assert.Equal(t, tc.want, window.CloseDirect())
		})
	}
}
