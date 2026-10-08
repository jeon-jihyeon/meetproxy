package guard

import (
	"net/url"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

// What the words after an HTTP program ask for
type request struct {
	writes bool
	urls   []string
	// A flag that sends the request through a place the URL does not name and empty when none does
	route string
}

var (
	// Programs that send HTTP requests and how their words read
	// A write to the Slack or GitHub API is already denied as a direct API call
	netTools = map[string]func(args []string, stdin string) request{
		"curl": curlRequest, "wget": wgetRequest, "http": httpieRequest, "https": httpieRequest, "xh": httpieRequest, "xhs": httpieRequest,
	}
	readMethods = map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true}
	// Methods httpie and xh take as their first positional word
	httpMethods = map[string]bool{
		"GET": true, "HEAD": true, "OPTIONS": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}
	// Short curl flags that take a value so the rest of a cluster such as -oout is that value
	curlValued = "AbcCdDeEFHKmoPQrtTuUwxXyYz"
	// Long curl flags that take a value so the next word is no URL
	curlLongValued = toSet("--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--json",
		"--form", "--form-string", "--url", "--request", "--upload-file", "--config", "--resolve", "--connect-to",
		"--proxy", "--preproxy", "--socks4", "--socks4a", "--socks5", "--socks5-hostname", "--unix-socket",
		"--abstract-unix-socket", "--doh-url", "--output", "--output-dir", "--header", "--user", "--user-agent",
		"--referer", "--cookie", "--cookie-jar", "--cert", "--key", "--cacert", "--capath", "--max-time",
		"--connect-timeout", "--retry", "--retry-delay", "--retry-max-time", "--write-out", "--range", "--limit-rate",
		"--proxy-user", "--oauth2-bearer", "--interface", "--dump-header", "--trace", "--trace-ascii", "--stderr",
		"--max-filesize", "--variable", "--url-query", "--aws-sigv4", "--max-redirs", "--proto", "--proto-redir",
		"--netrc-file", "--dns-servers", "--local-port")
	// curl flags that send a request somewhere its URL does not say
	curlRoutes = toSet("--resolve", "--connect-to", "--proxy", "--preproxy", "--socks4", "--socks4a", "--socks5",
		"--socks5-hostname", "--unix-socket", "--abstract-unix-socket", "--doh-url", "--config")
	// Short wget flags that take a value
	wgetValued = "aABDeIiloOPQRtTUwX"
	// Long wget flags that take a value when no = joins it
	wgetLongValued = toSet("--post-data", "--post-file", "--body-data", "--body-file", "--method", "--header",
		"--output-document", "--output-file", "--append-output", "--user", "--password", "--http-user",
		"--http-password", "--user-agent", "--referer", "--execute", "--input-file", "--directory-prefix", "--tries",
		"--timeout", "--wait", "--quota", "--level", "--accept", "--reject", "--domains", "--config",
		"--load-cookies", "--save-cookies", "--ca-certificate", "--certificate", "--private-key", "--bind-address")
	// Long httpie and xh flags that take a value when no = joins it
	httpieLongValued = toSet("--auth", "--auth-type", "--output", "--session", "--session-read-only", "--verify",
		"--cert", "--cert-key", "--cert-key-pass", "--ssl", "--ciphers", "--timeout", "--max-redirects", "--pretty",
		"--style", "--print", "--format-options", "--boundary", "--response-charset", "--response-mime",
		"--default-scheme", "--raw", "--proxy")
	// Short httpie and xh flags that take a value
	httpieValued = "aAops"
	// Programs that open a connection to any host and port
	rawSockets = toSet("nc", "ncat", "netcat", "socat", "telnet")
	// Programs that copy files to another host
	remoteCopies = toSet("scp", "rsync", "sftp")
)

func toSet(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

func isNetTool(w string) bool { return netTools[netName(w)] != nil }

// Windows names the same programs with .exe
func netName(w string) string { return strings.TrimSuffix(base(w), ".exe") }

// A request that sends data or names a method that writes posts to the host of every URL it names
// 1. A URL the guard cannot place such as one holding a variable is unknown
// 2. A request routed through a proxy, a resolve rule, a socket or a config file is unknown
// Every word naming such a program starts a call whatever wrapper comes before it
func netWrite(c shell.Command, proxied bool) ([]Post, error) {
	i := slices.IndexFunc(c.Args, isNetTool)
	if i < 0 {
		return nil, nil
	}
	tool := netName(c.Args[i])
	r := netTools[tool](c.Args[i+1:], c.Stdin)
	switch {
	case !r.writes:
		return nil, nil
	case r.route != "":
		return nil, unknown("a " + tool + " write through " + r.route)
	case proxied || slices.ContainsFunc(c.Args[:i], proxyVar.MatchString) || hasProxyEnv(c.Env):
		return nil, unknown("a " + tool + " write through a proxy")
	case len(r.urls) == 0:
		return nil, unknown("a " + tool + " write to a host the guard cannot place")
	}
	var posts []Post
	for _, u := range r.urls {
		loc, ok := dest.Parse(dest.HTTPS + ":" + urlHost(u))
		if !ok {
			return nil, unknown("a " + tool + " write to a host the guard cannot place")
		}
		posts = append(posts, Post{At: loc})
	}
	return posts, nil
}

func hasProxyEnv(env map[string]string) bool {
	for k := range env {
		if strings.HasSuffix(strings.ToLower(k), "_proxy") {
			return true
		}
	}
	return false
}

// The host of a URL in lower case without its port and empty when the guard cannot tell it
// A variable, a glob or a brace may name any host so it is never read
func urlHost(raw string) string {
	if strings.ContainsAny(raw, "$`{}[]*?") {
		return ""
	}
	if strings.HasPrefix(raw, ":") {
		raw = "localhost" + raw
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func writeMethod(m string) bool { return m != "" && !readMethods[strings.ToUpper(m)] }

// The words of one command line with each flag value skipped
type words struct {
	args []string
	i    int
}

// The value of the flag at i joined by = or in the next word
func (w *words) value(joined string, eq bool) string {
	if eq {
		return joined
	}
	if w.i+1 < len(w.args) {
		w.i++
		return w.args[w.i]
	}
	return ""
}

// 1. A method other than GET, HEAD or OPTIONS
// 2. Data unless -G sends it as a query
// 3. A form, an upload or options read from a config file
// URLs are the positional words and the values of --url
func curlRequest(args []string, _ string) request {
	var c curl
	ws := words{args: args}
	for ; ws.i < len(args); ws.i++ {
		w := args[ws.i]
		switch {
		case w == "--":
			c.urls = append(c.urls, args[ws.i+1:]...)
			ws.i = len(args)
		case strings.HasPrefix(w, "--"):
			name, v, eq := strings.Cut(w, "=")
			if curlLongValued[name] {
				v = ws.value(v, eq)
			}
			c.long(name, v)
		case len(w) > 1 && w[0] == '-':
			c.cluster(w[1:], &ws)
		default:
			c.urls = append(c.urls, w)
		}
	}
	c.writes = c.writes || writeMethod(c.method) || (c.data && !c.get)
	return c.request
}

// A curl request while its words are read
type curl struct {
	request
	method    string
	data, get bool
}

func (c *curl) long(name, v string) {
	switch {
	case name == "--url":
		c.urls = append(c.urls, v)
	case name == "--request":
		c.method = v
	case name == "--get":
		c.get = true
	case strings.HasPrefix(name, "--data") || name == "--json":
		c.data = true
	case strings.HasPrefix(name, "--form") || name == "--upload-file" || name == "--config":
		c.writes = true
	}
	if curlRoutes[name] {
		c.route = name
	}
}

// Reads a cluster of short curl flags such as -sSd until a flag that takes a value
func (c *curl) cluster(letters string, ws *words) {
	for i, l := range letters {
		if l == 'G' {
			c.get = true
			continue
		}
		if !strings.ContainsRune(curlValued, l) {
			continue
		}
		rest := letters[i+1:]
		v := ws.value(rest, rest != "")
		switch l {
		case 'X':
			c.method = v
		case 'd':
			c.data = true
		case 'F', 'T':
			c.writes = true
		case 'K':
			c.writes, c.route = true, "-K"
		case 'x':
			c.route = "-x"
		}
		return
	}
}

// Post or body data, a method that writes or startup commands that may set them
// URLs read from a file or a proxy set by -e cannot be placed
func wgetRequest(args []string, _ string) request {
	var r request
	ws := words{args: args}
	for ; ws.i < len(args); ws.i++ {
		w := args[ws.i]
		switch {
		case w == "--":
			r.urls = append(r.urls, args[ws.i+1:]...)
			ws.i = len(args)
		case strings.HasPrefix(w, "--"):
			name, v, eq := strings.Cut(w, "=")
			if wgetLongValued[name] {
				v = ws.value(v, eq)
			}
			r.wget(name, v)
		case len(w) > 1 && w[0] == '-':
			for i, c := range w[1:] {
				if !strings.ContainsRune(wgetValued, c) {
					continue
				}
				rest := w[i+2:]
				r.wget("-"+string(c), ws.value(rest, rest != ""))
				break
			}
		default:
			r.urls = append(r.urls, w)
		}
	}
	return r
}

func (r *request) wget(flag, v string) {
	switch flag {
	case "--post-data", "--post-file", "--body-data", "--body-file":
		r.writes = true
	case "--config":
		r.writes, r.route = true, flag
	case "--method":
		r.writes = r.writes || writeMethod(v)
	case "-i", "--input-file":
		r.route = flag
	case "-e", "--execute":
		c := strings.ToLower(v)
		r.writes = r.writes || strings.Contains(c, "post_") || strings.Contains(c, "body_") || strings.Contains(c, "method")
		if strings.Contains(c, "proxy") {
			r.route = flag
		}
	}
}

// httpie and xh take [METHOD] URL [ITEM...]
// 1. A method that writes
// 2. A data field such as a=b, a:=1 or a@file while headers a:b and queries a==b only read
// 3. A form, multipart or raw body or a here document as the body
func httpieRequest(args []string, stdin string) request {
	r := request{writes: stdin != ""}
	var pos []string
	ws := words{args: args}
	for ; ws.i < len(args); ws.i++ {
		w := args[ws.i]
		switch {
		case w == "--":
			pos = append(pos, args[ws.i+1:]...)
			ws.i = len(args)
		case strings.HasPrefix(w, "-") && len(w) > 1:
			r.httpieFlag(w, &ws)
		default:
			pos = append(pos, w)
		}
	}
	if len(pos) > 0 && httpMethods[strings.ToUpper(pos[0])] {
		r.writes = r.writes || writeMethod(pos[0])
		pos = pos[1:]
	}
	if len(pos) > 0 {
		r.urls = pos[:1]
		r.writes = r.writes || slices.ContainsFunc(pos[1:], dataItem)
	}
	return r
}

func (r *request) httpieFlag(w string, ws *words) {
	name, v, eq := strings.Cut(w, "=")
	switch {
	case w == "-f" || w == "--form" || w == "--multipart":
		r.writes = true
	case httpieLongValued[name]:
		ws.value(v, eq)
		r.writes = r.writes || name == "--raw"
		if name == "--proxy" {
			r.route = name
		}
	case len(w) == 2 && strings.ContainsRune(httpieValued, rune(w[1])):
		ws.value("", false)
	}
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

// Connections the guard cannot place
// 1. A raw socket program or a bash /dev/tcp path opens any host and port
// 2. sftp, and scp or rsync whose last word is HOST:PATH, copy files to another host
func remote(c shell.Command) error {
	if slices.ContainsFunc(slices.Concat(c.Args, c.Redirects), devSocket) {
		return unknown("a raw socket through /dev/tcp")
	}
	if i := slices.IndexFunc(c.Args, isRawSocket); i >= 0 {
		return unknown("a raw socket through " + netName(c.Args[i]))
	}
	i := slices.IndexFunc(c.Args, isRemoteCopy)
	if i < 0 {
		return nil
	}
	tool := netName(c.Args[i])
	if tool == "sftp" {
		return unknown("an sftp session")
	}
	last := ""
	for _, w := range c.Args[i+1:] {
		if !strings.HasPrefix(w, "-") {
			last = w
		}
	}
	if remotePath(last) {
		return unknown("a " + tool + " copy to another host")
	}
	return nil
}

func isRawSocket(w string) bool  { return rawSockets[netName(w)] }
func isRemoteCopy(w string) bool { return remoteCopies[netName(w)] }
func devSocket(w string) bool {
	return strings.Contains(w, "/dev/tcp/") || strings.Contains(w, "/dev/udp/")
}
func remotePath(w string) bool {
	if strings.HasPrefix(w, "rsync://") {
		return true
	}
	i := strings.IndexByte(w, ':')
	return i > 0 && !strings.Contains(w[:i], "/")
}
