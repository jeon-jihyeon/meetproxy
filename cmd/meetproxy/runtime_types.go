package main

import "regexp"

var sessionId = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
