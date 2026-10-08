package guard

import (
	"cmp"
	"net/url"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

var (
	// Programs that send HTTP requests and whether the words after them write
	// A write to the Slack or GitHub API is already denied as a direct API call so any other write is one the guard cannot place
	netTools = map[string]func(args []string, stdin string) bool{
		"curl": curlWrites, "wget": wgetWrites, "http": httpieWrites, "https": httpieWrites, "xh": httpieWrites, "xhs": httpieWrites,
	}
	readMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true}
	// Methods httpie and xh take as their first positional word
	httpMethods = map[string]bool{
		"GET": true, "HEAD": true, "OPTIONS": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}
	// Short curl flags that take a value so the rest of a cluster such as -oout is that value
	curlValued = "AbcCDeEHKmoPQrtuUwxXyYz"
)

func isNetTool(w string) bool { return netTools[netName(w)] != nil }

// Windows names the same programs with .exe
func netName(w string) string { return strings.TrimSuffix(base(w), ".exe") }

// A request that sends data or names a method that writes
// Every word naming such a program starts a call whatever wrapper comes before it
func netWrite(c shell.Command) error {
	i := slices.IndexFunc(c.Args, isNetTool)
	if i < 0 {
		return nil
	}
	tool := netName(c.Args[i])
	if !netTools[tool](c.Args[i+1:], c.Stdin) {
		return nil
	}
	if h := host(c.Args[i+1:]); h != "" {
		return unknown("a " + tool + " write to " + h)
	}
	return unknown("a " + tool + " write to a host the guard cannot place")
}

func writeMethod(m string) bool { return m != "" && !readMethods[strings.ToUpper(m)] }

// The host of the first link among the words or empty when none names one
func host(args []string) string {
	for _, w := range args {
		if !strings.Contains(w, "://") {
			continue
		}
		if u, err := url.Parse(w); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}

// 1. A method other than GET, HEAD or OPTIONS
// 2. Data unless -G sends it as a query
// 3. A form, an upload or options read from a config file
func curlWrites(args []string, _ string) bool {
	var method string
	data, get := false, false
	for i, w := range args {
		var m string
		var d, g, writes bool
		switch {
		case w == "-X" || w == "--request":
			if i+1 < len(args) {
				m = args[i+1]
			}
		case strings.HasPrefix(w, "--"):
			m, d, g, writes = curlLong(w)
		case len(w) > 1 && w[0] == '-':
			m, d, g, writes = curlCluster(w[1:])
		}
		if writes {
			return true
		}
		method, data, get = cmp.Or(m, method), data || d, get || g
	}
	return writeMethod(method) || (data && !get)
}

func curlLong(w string) (method string, data, get, writes bool) {
	switch {
	case strings.HasPrefix(w, "--request="):
		return strings.TrimPrefix(w, "--request="), false, false, false
	case w == "--get":
		return "", false, true, false
	case strings.HasPrefix(w, "--data"), w == "--json", strings.HasPrefix(w, "--json="):
		return "", true, false, false
	case strings.HasPrefix(w, "--form"), strings.HasPrefix(w, "--upload-file"), strings.HasPrefix(w, "--config"):
		return "", false, false, true
	}
	return "", false, false, false
}

// Reads a cluster of short curl flags such as -sSd until a flag that takes a value
func curlCluster(letters string) (method string, data, get, writes bool) {
	for i, c := range letters {
		switch {
		case c == 'F' || c == 'T' || c == 'K':
			return "", false, false, true
		case c == 'd':
			return "", true, get, false
		case c == 'G':
			get = true
		case c == 'X':
			return letters[i+1:], false, get, false
		case strings.ContainsRune(curlValued, c):
			return "", false, get, false
		}
	}
	return "", false, get, false
}

// Post or body data, a method that writes or startup commands that may set them
func wgetWrites(args []string, _ string) bool {
	for i, w := range args {
		name, v, eq := strings.Cut(w, "=")
		if !eq && i+1 < len(args) {
			v = args[i+1]
		}
		switch name {
		case "--post-data", "--post-file", "--body-data", "--body-file", "--config":
			return true
		case "--method":
			if writeMethod(v) {
				return true
			}
		case "-e", "--execute":
			if wgetrcWrites(v) {
				return true
			}
		}
	}
	return false
}

func wgetrcWrites(cmd string) bool {
	c := strings.ToLower(cmd)
	return strings.Contains(c, "post_") || strings.Contains(c, "body_") || strings.Contains(c, "method")
}

// httpie and xh take [METHOD] URL [ITEM...]
// 1. A method that writes
// 2. A data field such as a=b, a:=1 or a@file while headers a:b and queries a==b only read
// 3. A form, multipart or raw body or a here document as the body
func httpieWrites(args []string, stdin string) bool {
	if stdin != "" {
		return true
	}
	var pos []string
	for i, w := range args {
		if w == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		switch {
		case w == "-f" || w == "--form" || w == "--multipart" || strings.HasPrefix(w, "--raw"):
			return true
		case strings.HasPrefix(w, "-") && len(w) > 1:
		default:
			pos = append(pos, w)
		}
	}
	if len(pos) > 0 && httpMethods[strings.ToUpper(pos[0])] {
		if writeMethod(pos[0]) {
			return true
		}
		pos = pos[1:]
	}
	if len(pos) > 0 {
		pos = pos[1:]
	}
	return slices.ContainsFunc(pos, dataItem)
}

// The separator found first decides what an item is
func dataItem(item string) bool {
	i := strings.IndexAny(item, "=:@")
	if i < 0 {
		return false
	}
	next := byte(0)
	if i+1 < len(item) {
		next = item[i+1]
	}
	switch item[i] {
	case ':':
		return next == '='
	case '=':
		return next != '='
	}
	return true
}
