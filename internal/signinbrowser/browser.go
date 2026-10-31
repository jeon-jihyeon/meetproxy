package signinbrowser

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrAutomationDenied = errors.New("the browser's Automation permission for meetproxy was denied")
	ErrScript           = errors.New("the browser script failed")
	windowID            = regexp.MustCompile(`^[0-9]{1,20}$`)
	apps                = map[string]string{
		"google chrome": "Google Chrome",
		"chrome":        "Google Chrome",
		"whale":         "Whale",
		"naver whale":   "Whale",
	}
)

type Client struct {
	run func(context.Context, string) (string, error)
}

type Page struct {
	Gone        bool
	Known       bool
	Loopback    bool
	Loading     bool
	Single      bool
	Fingerprint string
	Origin      string
}

func New(run func(context.Context, string) (string, error)) Client { return Client{run: run} }

func (c Client) App(name string) (string, bool) {
	app, ok := apps[strings.ToLower(strings.TrimSpace(name))]
	return app, ok && c.run != nil
}

func (c Client) Valid(app, id string) bool {
	_, ok := apps[strings.ToLower(app)]
	return ok && windowID.MatchString(id) && c.run != nil
}

func (c Client) Available() bool { return c.run != nil }

func (c Client) Open(ctx context.Context, app, link string) (string, error) {
	if !c.Valid(app, "1") {
		return "", ErrScript
	}
	id, err := c.run(ctx, openScript(app, link))
	if err != nil {
		return "", err
	}
	if !windowID.MatchString(id) {
		return "", ErrScript
	}
	return id, nil
}

func (c Client) Read(ctx context.Context, app, id string) (Page, error) {
	if !c.Valid(app, id) {
		return Page{}, ErrScript
	}
	out, err := c.run(ctx, readScript(app, id))
	if err != nil {
		return Page{}, err
	}
	if out == gone {
		return Page{Gone: true}, nil
	}
	loading, address, _ := strings.Cut(out, " ")
	page := describe(address)
	page.Loading = loading == "true"
	return page, nil
}

func (c Client) Inspect(ctx context.Context, app, id string) (Page, error) {
	if !c.Valid(app, id) {
		return Page{}, ErrScript
	}
	out, err := c.run(ctx, tabsScript(app, id))
	if err != nil {
		return Page{}, err
	}
	count, address, _ := strings.Cut(out, " ")
	page := describe(address)
	page.Single = count == "1"
	return page, nil
}

func (c Client) Close(ctx context.Context, app, id string) (bool, error) {
	if !c.Valid(app, id) {
		return false, ErrScript
	}
	out, err := c.run(ctx, closeScript(app, id))
	return err == nil && out == gone, err
}

func describe(address string) Page {
	u, err := url.Parse(address)
	return Page{
		Known:    err == nil && (u.Scheme == "http" || u.Scheme == "https"),
		Loopback: isLoopback(address), Fingerprint: Fingerprint(address), Origin: Origin(address),
	}
}
