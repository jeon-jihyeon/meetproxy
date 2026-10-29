//go:build windows

package main

import "time"

// Windows has no browser scripting meetproxy uses
func osBrowser() browserScript { return browserScript{clock: time.Now, sleep: time.Sleep} }
