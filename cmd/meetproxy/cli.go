package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type cli struct {
	data    string
	session string
	now     time.Time
	out     io.Writer
}

func (c cli) open(origin string) error {
	r, err := relay.New(c.data).Open(c.session, origin, c.now)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, r.Id)
	return nil
}

func (c cli) locate(terms []string, limit int) error {
	cs, err := locmap.New(c.data).Locate(terms, limit)
	if err != nil {
		return err
	}
	for _, cand := range cs {
		fmt.Fprintf(c.out, "%d\t%s\t%s\t%s\n", cand.Score, cand.Repo, cand.Abs(), strings.Join(cand.Topics, " | "))
	}
	return nil
}

func (c cli) dest(loc string) (bool, error) {
	allowed, err := dest.New(c.data).Allowed(loc)
	if err != nil {
		return false, err
	}
	if allowed {
		fmt.Fprintln(c.out, "allowed")
	} else {
		fmt.Fprintln(c.out, "denied")
	}
	return allowed, nil
}

func (c cli) allow(pattern string) error {
	if _, ok := dest.Location(pattern); !ok {
		return fmt.Errorf("pattern must look like github:owner/repo or slack:CHANNEL %q", pattern)
	}
	return dest.New(c.data).Add(pattern)
}

func (c cli) close(topic string, keywords, paths []string) error {
	store := relay.New(c.data)
	r, err := store.Current(c.session)
	if err != nil {
		return err
	}
	ps, err := store.Evidence(r.Id, paths)
	if err != nil {
		return err
	}
	e := locmap.Entry{RelayId: r.Id, Topic: topic, Keywords: keywords, Paths: ps, RecordedAt: c.now.UTC()}
	if err := locmap.New(c.data).Add(e); err != nil {
		return err
	}
	if _, err := store.Close(c.session, c.now); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s closed · %d paths\n", r.Id, len(ps))
	return nil
}
