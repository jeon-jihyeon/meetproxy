package signinbrowser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	gone          = "gone"
	signinLogTail = 1 << 20
)

var httpLink = regexp.MustCompile(`http://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

func Fingerprint(link string) string {
	sum := sha256.Sum256([]byte(browserForm(link)))
	return hex.EncodeToString(sum[:])
}

func browserForm(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), defaultPort(u)
	if port != "" {
		host += ":" + port
	}
	path := fallback(u.Path, "/")
	return scheme + "://" + host + path + "?" + u.Query().Encode() + "#" + u.Fragment
}

func defaultPort(u *url.URL) string {
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		return ""
	}
	return port
}

func Origin(link string) string {
	u, err := url.Parse(link)
	if err != nil || !isLoopback(link) {
		return ""
	}
	sum := sha256.Sum256([]byte("http://loopback:" + fallback(defaultPort(u), "80")))
	return hex.EncodeToString(sum[:])
}

func LinkAfter(log string, offset int64) string {
	f, err := os.Open(log)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || offset > info.Size() {
		return ""
	}
	from := max(offset, info.Size()-signinLogTail)
	b, err := io.ReadAll(io.NewSectionReader(f, from, info.Size()-from))
	if err != nil {
		return ""
	}
	return newestLoopbackLink(b)
}

func newestLoopbackLink(b []byte) string {
	links := httpLink.FindAll(b, -1)
	for i := len(links) - 1; i >= 0; i-- {
		link := strings.TrimRight(string(links[i]), ".,;:!?')]")
		if isLoopback(link) && !strings.Contains(link, "@") {
			return link
		}
	}
	return ""
}

func isLoopback(link string) bool {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}

func openScript(app, link string) string {
	return fmt.Sprintf(`tell application %s
	set w to make new window
	set URL of active tab of w to %s
	return id of w
end tell
`, appleString(app), appleString(link))
}

func readScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then
			set t to active tab of window id %[2]s
			return (loading of t as text) & " " & (URL of t)
		end if
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(gone))
}

func tabsScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then
			set w to window id %[2]s
			return ((count of tabs of w) as text) & " " & (URL of active tab of w)
		end if
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(gone))
}

func closeScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then close window id %[2]s
		if exists window id %[2]s then return "open"
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(gone))
}

func fallback(value, other string) string {
	if value != "" {
		return value
	}
	return other
}

func appleString(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
