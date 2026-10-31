package signin

import "time"

type Stage uint8

const (
	Initial Stage = iota
	Away
	Back
	Done
	Unknown
)

const (
	GiveUp = 5 * time.Minute
	Keep   = 10 * time.Minute
)

type Observation struct {
	Known       bool
	Loopback    bool
	Loading     bool
	Fingerprint string
	Origin      string
}

type Window struct {
	at       time.Time
	stage    Stage
	printed  string
	origin   string
	scripted bool
	tracked  bool
	direct   bool
}

func RestoreWindow(at time.Time, stage Stage, printed, origin string, scripted bool) Window {
	return Window{at: at, stage: stage, printed: printed, origin: origin, scripted: scripted}
}

func (w Window) Stage() Stage { return w.stage }

func (w Window) CloseDirect() bool { return w.direct }

func (w Window) ReadFailed() Window {
	w.tracked = false
	w.direct = false
	return w
}

func (w Window) Expired(now time.Time) bool { return !now.Before(w.at.Add(GiveUp)) }

func (w Window) MayInspect(now time.Time) bool {
	return w.scripted && w.origin != "" && now.Before(w.at.Add(Keep))
}

func (w Window) onOrigin(origin string) bool { return w.origin != "" && origin == w.origin }

func (w Window) Owns(single bool, origin string) bool { return single && w.onOrigin(origin) }

func (w Window) Observe(o Observation) Window {
	w.direct = w.tracked
	w.tracked = w.onOrigin(o.Origin)
	if !o.Known {
		return w
	}
	if w.stage == Initial && o.Loopback && w.printed != "" && o.Fingerprint != w.printed {
		w.stage = Away
	}
	switch {
	case w.stage == Initial && !o.Loopback:
		w.stage = Away
	case w.stage == Away && o.Loopback && o.Loading:
		w.stage = Back
	case w.stage == Away && o.Loopback, w.stage == Back && (!o.Loading || !o.Loopback):
		w.stage = Done
	}
	return w
}

type Closure struct {
	open bool
	keep bool
}

func (w Window) AfterClose(gone bool, origin string) Closure {
	return Closure{open: !gone, keep: !gone && w.onOrigin(origin)}
}

func (c Closure) Open() bool        { return c.open }
func (c Closure) KeepsRecord() bool { return c.keep }
