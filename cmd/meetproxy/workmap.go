package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/workmap"
)

// With daily every session start can ask for a refresh and only the first of the day runs it
// A refresh another session is running already covers this one
func (c cli) mapRefresh(daily bool) error {
	config, err := configDir()
	if err != nil {
		return err
	}
	done, err := workmap.New(c.data).Refresh(config, daily, c.now)
	if errors.Is(err, workmap.ErrBusy) {
		return nil
	}
	if err == nil && done {
		fmt.Fprintln(c.out, "refreshed")
	}
	return err
}

func (c cli) mapShow() error {
	store := workmap.New(c.data)
	places, err := store.Places()
	if err != nil {
		return err
	}
	methods, err := store.Methods()
	if err != nil {
		return err
	}
	for _, p := range places {
		fmt.Fprintf(c.out, "%d\t%s\t%s\t%v\n", p.Cases, p.Name, p.Root, p.Aliases)
	}
	fmt.Fprintf(c.out, "%d skills and commands\n", len(methods))
	formats, err := store.Formats()
	if err != nil {
		return err
	}
	for _, f := range formats {
		fmt.Fprintf(c.out, "%s\t%d guides\t%d skills\t%d examples\n", f.Kind, len(f.Guides), len(f.Skills), len(f.Examples))
	}
	return nil
}

// Sections in order
// 1. guides as the file and line followed by the heading
// 2. skills with their description
// 3. examples each under a line naming its time and place
// A section without entries is left out so a kind the map knows nothing of prints nothing
func (c cli) mapFormat(kind, place string) error {
	all, err := workmap.New(c.data).Format(kind)
	if errors.Is(err, workmap.ErrUnknownKind) {
		return fmt.Errorf("%w: %w, one of %v", errUsage, err, workmap.Kinds)
	}
	if err != nil {
		return err
	}
	f := all.In(place)
	sections := 0
	section := func(name string, n int) {
		if n == 0 {
			return
		}
		if sections > 0 {
			fmt.Fprintln(c.out)
		}
		sections++
		fmt.Fprintf(c.out, "%s:\n", name)
	}
	section("guides", len(f.Guides))
	for _, g := range f.Guides {
		fmt.Fprintf(c.out, "%s:%d %s\n", g.File, g.Line, g.Heading)
	}
	section("skills", len(f.Skills))
	for _, m := range f.Skills {
		fmt.Fprintf(c.out, "%s: %s\n", m.Name, m.Description)
	}
	section("examples", len(f.Examples))
	for i, e := range f.Examples {
		fmt.Fprintf(c.out, "--- example %d of %d, %s, %s\n%s\n", i+1, len(f.Examples), e.At.Format(time.RFC3339), e.Root, e.Text)
	}
	return nil
}
