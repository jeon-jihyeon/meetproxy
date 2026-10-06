// Package shell splits shell command text into simple commands the way a shell reads them
package shell

import (
	"regexp"
	"strings"
)

// One simple command of shell text
type Command struct {
	Args []string
	// Assignments written before the command name
	Env map[string]string
	// Body of the here document or here-string the command reads
	Stdin string
}

// The simple commands of shell text in order
// 1. Quotes are removed and a backslash escapes the next character outside single quotes
// 2. ;, &, |, &&, ||, parentheses and line breaks end a command and a pipe passes no Stdin on
// 3. Comments and backslash line breaks are dropped
// 4. Here document bodies are never read as commands and become the Stdin of the command declaring them
// 5. "$(cat <<'EOF' ... EOF)" becomes the body of its here document
// 6. Other substitutions, backticks, arithmetic and ${...} stay as written inside their word
// 7. Redirections and their targets are not words of the command
func Commands(text string) []Command {
	l := lexer{s: text}
	l.run()
	return l.out
}

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	parenDepth = map[byte]int{'(': 1, ')': -1}
)

// Where the next word goes
type target int

const (
	toArgs    target = iota
	toStdin          // the word after <<<
	toNowhere        // the target of a redirection
)

type lexer struct {
	s string
	i int
	// Inside $( ) or a process substitution
	nested bool
	// Open parentheses inside $( )
	parens int
	word   strings.Builder
	inWord bool
	// Offset in s where the current word starts
	start int
	next  target
	cmd   Command
	// The current command declared a here document or here-string
	reads bool
	out   []Command
	// Here documents declared on the current line in order
	docs []heredoc
}

type heredoc struct {
	delim string
	// Leading tabs of the body and the end line are dropped with <<-
	strip bool
	// Index in out of the command that reads it
	owner int
}

func (l *lexer) run() {
	for l.i < len(l.s) && !l.closed() {
		if !l.separator() {
			l.step()
		}
	}
	l.endCommand()
}

// Whether an unmatched ) ends the text of $( )
func (l *lexer) closed() bool {
	return l.nested && l.parens == 0 && l.s[l.i] == ')'
}

func (l *lexer) step() {
	switch c := l.s[l.i]; c {
	case '\\':
		l.escaped()
	case '\'':
		l.singleQuoted()
	case '"':
		l.doubleQuoted()
	case '$', '`':
		l.expansion()
	default:
		l.add(c)
	}
}

// Handles blanks, line breaks, comments, operators and redirections and reports whether it did
func (l *lexer) separator() bool {
	c := l.s[l.i]
	rest := l.s[l.i:]
	switch {
	case c == ' ' || c == '\t':
		l.endWord()
		l.i++
	case c == '\n':
		l.endCommand()
		l.i++
		l.readDocs()
	case c == '#' && !l.inWord:
		l.i += lineEnd(rest)
	case strings.HasPrefix(rest, "<(") || strings.HasPrefix(rest, ">("):
		l.substitution()
	case c == '<' || c == '>' || strings.HasPrefix(rest, "&>"):
		l.redirection()
	case strings.HasPrefix(rest, "((") && !l.inWord && len(l.cmd.Args) == 0:
		l.raw(balanced(l.s, l.i, '(', ')'))
	case strings.IndexByte(";&|()", c) >= 0:
		l.endCommand()
		l.parens += parenDepth[c]
		l.i++
	default:
		return false
	}
	return true
}

// A redirection from < or > or &> up to its operator end
// 1. <<< makes the next word the Stdin
// 2. << and <<- declare a here document
// 3. A file descriptor number right before the operator is dropped with the target word
func (l *lexer) redirection() {
	rest := l.s[l.i:]
	switch {
	case strings.HasPrefix(rest, "<<<"):
		l.endWord()
		l.i += 3
		l.next = toStdin
		return
	case strings.HasPrefix(rest, "<<"):
		l.heredoc()
		return
	}
	if l.inWord && strings.Trim(l.s[l.start:l.i], "0123456789") == "" {
		l.word.Reset()
		l.inWord = false
	}
	l.endWord()
	if rest[0] == '&' {
		l.i++
	}
	l.i++
	if l.i < len(l.s) && strings.IndexByte(">|&", l.s[l.i]) >= 0 {
		l.i++
	}
	l.next = toNowhere
}

func (l *lexer) heredoc() {
	l.endWord()
	delim, strip, next := delimiter(l.s, l.i+2)
	l.i = next
	if delim != "" {
		l.docs = append(l.docs, heredoc{delim, strip, len(l.out)})
		l.reads = true
	}
}

// The bodies of the here documents declared on the line that just ended
// A body without its end line runs to the end of the text
func (l *lexer) readDocs() {
	for _, d := range l.docs {
		body, end, ok := docBody(l.s, l.i, d.delim, d.strip)
		if !ok {
			body, end = l.s[l.i:], len(l.s)
		}
		if d.owner < len(l.out) {
			l.out[d.owner].Stdin = body
		}
		l.i = end
	}
	l.docs = nil
}

func (l *lexer) begin() {
	if !l.inWord {
		l.inWord = true
		l.start = l.i
	}
}

func (l *lexer) add(c byte) {
	l.begin()
	l.word.WriteByte(c)
	l.i++
}

// Copies s up to end into the word as written
func (l *lexer) raw(end int) {
	l.begin()
	l.word.WriteString(l.s[l.i:end])
	l.i = end
}

// A backslash and a line break join lines and before any other character keep it as it is
func (l *lexer) escaped() {
	if l.i+1 >= len(l.s) {
		l.i++
		return
	}
	if l.s[l.i+1] != '\n' {
		l.begin()
		l.word.WriteByte(l.s[l.i+1])
	}
	l.i += 2
}

func (l *lexer) singleQuoted() {
	l.begin()
	end := strings.IndexByte(l.s[l.i+1:], '\'')
	if end < 0 {
		end = len(l.s) - l.i - 1
	}
	l.word.WriteString(l.s[l.i+1 : l.i+1+end])
	l.i = min(l.i+end+2, len(l.s))
}

func (l *lexer) doubleQuoted() {
	l.begin()
	l.i++
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == '"':
			l.i++
			return
		case c == '\\' && l.i+1 < len(l.s) && strings.IndexByte("\"\\$`\n", l.s[l.i+1]) >= 0:
			if l.s[l.i+1] != '\n' {
				l.word.WriteByte(l.s[l.i+1])
			}
			l.i += 2
		case c == '$' || c == '`':
			l.expansion()
		default:
			l.word.WriteByte(c)
			l.i++
		}
	}
}

// A $ or backtick expansion kept as written except for "$(cat <<EOF ... EOF)"
func (l *lexer) expansion() {
	rest := l.s[l.i:]
	switch {
	case strings.HasPrefix(rest, "$(("):
		l.raw(balanced(l.s, l.i+1, '(', ')'))
	case strings.HasPrefix(rest, "$("):
		l.substitution()
	case strings.HasPrefix(rest, "${"):
		l.raw(balanced(l.s, l.i+1, '{', '}'))
	case rest[0] == '`':
		l.raw(backtickEnd(l.s, l.i))
	default:
		l.add(rest[0])
	}
}

// $( ), <( ) or >( ) read by a nested lexer so quotes, comments and here documents inside never end it early
// A lone cat reading a here document becomes the body of that here document
func (l *lexer) substitution() {
	inner := lexer{s: l.s, i: l.i + 2, nested: true}
	inner.run()
	end := min(inner.i+1, len(l.s))
	if len(inner.out) == 1 && inner.out[0].Stdin != "" && strings.Join(inner.out[0].Args, " ") == "cat" {
		l.begin()
		l.word.WriteString(inner.out[0].Stdin)
		l.i = end
		return
	}
	l.raw(end)
}

func (l *lexer) endWord() {
	if !l.inWord {
		return
	}
	w := l.word.String()
	l.word.Reset()
	l.inWord = false
	switch {
	case l.next == toStdin:
		l.cmd.Stdin = w
		l.reads = true
	case l.next == toNowhere:
	case len(l.cmd.Args) == 0 && assignment.MatchString(l.s[l.start:]):
		name, value, _ := strings.Cut(w, "=")
		if l.cmd.Env == nil {
			l.cmd.Env = map[string]string{}
		}
		l.cmd.Env[name] = value
	default:
		l.cmd.Args = append(l.cmd.Args, w)
	}
	l.next = toArgs
}

func (l *lexer) endCommand() {
	l.endWord()
	l.next = toArgs
	if len(l.cmd.Args) > 0 || len(l.cmd.Env) > 0 || l.reads {
		l.out = append(l.out, l.cmd)
	}
	l.cmd = Command{}
	l.reads = false
}

// Length of s up to its first line break or all of it
func lineEnd(s string) int {
	if n := strings.IndexByte(s, '\n'); n >= 0 {
		return n
	}
	return len(s)
}

// Offset just after the close that matches the open at s[i] or the end of s
func balanced(s string, i int, open, close byte) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
		}
		if depth == 0 {
			return i + 1
		}
	}
	return len(s)
}

// Offset just after the backtick that closes the one at s[i] or the end of s
func backtickEnd(s string, i int) int {
	for i++; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '`':
			return i + 1
		}
	}
	return len(s)
}

// The end word of a here document from i just after << and where it ends
// Quotes and backslashes in the word are removed
func delimiter(s string, i int) (delim string, strip bool, next int) {
	if i < len(s) && s[i] == '-' {
		strip = true
		i++
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	var b strings.Builder
	for i < len(s) && strings.IndexByte(" \t\n;&|<>()", s[i]) < 0 {
		switch c := s[i]; c {
		case '\'', '"':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				end = len(s) - i - 1
			}
			b.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case '\\':
			if i+1 < len(s) {
				b.WriteByte(s[i+1])
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), strip, min(i, len(s))
}

// The lines from start up to the line that is delim and where that line ends
func docBody(s string, start int, delim string, strip bool) (body string, next int, ok bool) {
	var lines []string
	for pos := start; pos < len(s); {
		n := lineEnd(s[pos:])
		line := s[pos : pos+n]
		if strip {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delim {
			return strings.Join(lines, "\n"), min(pos+n+1, len(s)), true
		}
		lines = append(lines, line)
		pos += n + 1
	}
	return "", len(s), false
}
