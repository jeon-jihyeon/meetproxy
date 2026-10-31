package slackstate

import "time"

const Paused = "collection paused by the user; resume with the source tool"

type Health struct {
	ok      bool
	failure string
	found   int
	at      time.Time
}

func RestoreHealth(ok bool, failure string, found int, at time.Time) Health {
	return Health{ok: ok, failure: failure, found: found, at: at}
}

func (h Health) Failed() bool { return !h.ok && h.failure != "" && h.failure != Paused }

func (h Health) NeedsUpdate(ok bool, failure string, found int, now time.Time) bool {
	return h.ok != ok || h.failure != failure || h.found != found || now.Sub(h.at) >= 10*time.Minute
}

func (h Health) Collect(paused bool) Health {
	if paused {
		return Health{failure: Paused, at: h.at}
	}
	h.ok = h.failure == ""
	return h
}

func (h Health) OK() bool      { return h.ok }
func (h Health) Error() string { return h.failure }
func (h Health) Found() int    { return h.found }
func (h Health) At() time.Time { return h.at }
