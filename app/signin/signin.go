package signin

import (
	"context"
	"errors"
	"time"

	model "github.com/jeon-jihyeon/meetproxy/internal/signin"
	"github.com/jeon-jihyeon/meetproxy/internal/signinbrowser"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
)

var (
	ErrAutomationDenied = signinbrowser.ErrAutomationDenied
	ErrBrowserScript    = signinbrowser.ErrScript
	ErrNoWindow         = errors.New("no sign-in window is open for this session")
)

const (
	runTime     = 90 * time.Second
	limitTime   = 100 * time.Second
	poll        = 500 * time.Millisecond
	openTimeout = 2 * time.Minute
	readTimeout = 10 * time.Second
)

type Config struct {
	Data    string
	Session string
	Now     time.Time
}

type Runtime struct {
	Run       func(context.Context, string) (string, error)
	Clock     func() time.Time
	Sleep     func(time.Duration)
	MarkClock func() time.Time
}

type Service struct {
	config  Config
	runtime Runtime
	browser signinbrowser.Client
	files   store.Signins
}

func SystemRuntime() Runtime {
	return Runtime{Run: signinbrowser.SystemRun(), Clock: time.Now, Sleep: time.Sleep, MarkClock: time.Now}
}

func New(config Config, runtime Runtime) *Service {
	if runtime.Clock == nil {
		runtime.Clock = time.Now
	}
	if runtime.Sleep == nil {
		runtime.Sleep = time.Sleep
	}
	if runtime.MarkClock == nil {
		runtime.MarkClock = time.Now
	}
	return &Service{config: config, runtime: runtime, browser: signinbrowser.New(runtime.Run),
		files: store.NewSignins(config.Data)}
}

func (s *Service) Open(log string, offset int64, browser string) (string, error) {
	app, available := s.browser.App(browser)
	if !available || offset < 0 {
		return "not found", nil
	}
	link := signinbrowser.LinkAfter(log, offset)
	if link == "" {
		return "not found", nil
	}
	if old, found, err := s.files.Read(s.config.Session); err == nil && found {
		s.close(old.Browser, old.Window, s.window(old), s.config.Now, time.Time{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	id, err := s.browser.Open(ctx, app, link)
	if err != nil {
		return "not found", nil
	}
	row := store.Signin{Browser: app, Window: id, At: s.config.Now,
		Printed: signinbrowser.Fingerprint(link), Origin: signinbrowser.Origin(link)}
	if err := s.write(row); err != nil {
		s.closeWindow(row.Browser, row.Window, time.Time{})
		return "", err
	}
	return "opened", nil
}

func (s *Service) Wait() (string, error) {
	row, found, err := s.files.Read(s.config.Session)
	if err != nil {
		return "", err
	}
	if !found || !s.browser.Available() {
		return "", ErrNoWindow
	}
	start := s.runtime.Clock()
	end, limit := start.Add(runTime), start.Add(limitTime)
	window := s.window(row)
	for {
		now := s.runtime.Clock()
		if window.Expired(now) {
			return s.finish("timed out", s.close(row.Browser, row.Window, window, now, limit))
		}
		if !now.Before(end) {
			return "waiting", nil
		}
		page, err := s.read(row.Browser, row.Window, limit)
		if errors.Is(err, ErrAutomationDenied) {
			return "", err
		}
		if page.Gone {
			return s.finish("closed", model.Closure{})
		}
		if err != nil {
			window = window.ReadFailed()
		} else {
			window = window.Observe(model.Observation{Known: page.Known, Loopback: page.Loopback,
				Loading: page.Loading, Fingerprint: page.Fingerprint, Origin: page.Origin})
		}
		if window.Stage() == model.Done {
			if window.CloseDirect() {
				gone := s.closeWindow(row.Browser, row.Window, limit)
				return s.finish("closed", window.AfterClose(gone, page.Origin))
			}
			return s.finish("closed", s.close(row.Browser, row.Window, window, now, limit))
		}
		if step := storedStage(window.Stage(), row.Step); step != row.Step {
			row.Step = step
			if err := s.write(row); err != nil {
				return "", err
			}
		}
		s.runtime.Sleep(poll)
	}
}

func (s *Service) End() error {
	row, found, readErr := s.files.Read(s.config.Session)
	err := s.files.Remove(s.config.Session)
	if readErr == nil && found {
		s.close(row.Browser, row.Window, s.window(row), s.config.Now, time.Time{})
	}
	return err
}

func (s *Service) window(row store.Signin) model.Window {
	return model.RestoreWindow(row.At, restoredStage(row.Step), row.Printed, row.Origin,
		s.browser.Valid(row.Browser, row.Window))
}

func (s *Service) write(row store.Signin) error {
	return s.files.Write(s.config.Session, row)
}

func (s *Service) finish(word string, closed model.Closure) (string, error) {
	if !closed.KeepsRecord() {
		if err := s.files.Remove(s.config.Session); err != nil {
			return "", err
		}
	}
	if closed.Open() {
		return "not closed", nil
	}
	return word, nil
}

func (s *Service) close(browser, id string, window model.Window, now, limit time.Time) model.Closure {
	if !window.MayInspect(now) {
		return model.Closure{}
	}
	ctx, cancel := s.scriptContext(limit)
	defer cancel()
	page, err := s.browser.Inspect(ctx, browser, id)
	if err != nil || !window.Owns(page.Single, page.Origin) {
		return model.Closure{}
	}
	return window.AfterClose(s.closeWindow(browser, id, limit), page.Origin)
}

func (s *Service) closeWindow(browser, id string, limit time.Time) bool {
	if !s.browser.Valid(browser, id) {
		return false
	}
	ctx, cancel := s.scriptContext(limit)
	defer cancel()
	gone, _ := s.browser.Close(ctx, browser, id)
	return gone
}

func (s *Service) read(browser, id string, limit time.Time) (signinbrowser.Page, error) {
	ctx, cancel := s.scriptContext(limit)
	defer cancel()
	return s.browser.Read(ctx, browser, id)
}

func (s *Service) scriptContext(limit time.Time) (context.Context, context.CancelFunc) {
	duration := readTimeout
	if !limit.IsZero() {
		duration = min(duration, limit.Sub(s.runtime.Clock()))
	}
	return context.WithTimeout(context.Background(), duration)
}

func restoredStage(step string) model.Stage {
	switch step {
	case "":
		return model.Initial
	case "away":
		return model.Away
	case "back":
		return model.Back
	case "closed":
		return model.Done
	default:
		return model.Unknown
	}
}

func storedStage(stage model.Stage, original string) string {
	switch stage {
	case model.Initial:
		return ""
	case model.Away:
		return "away"
	case model.Back:
		return "back"
	case model.Done:
		return "closed"
	default:
		return original
	}
}
