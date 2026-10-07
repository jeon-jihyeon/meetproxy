package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

func TestCommands(t *testing.T) {
	t.Parallel()
	type cmd = shell.Command
	args := func(a ...string) cmd { return cmd{Args: a} }
	tcs := []struct {
		name string
		text string
		want []cmd
	}{
		{"splits words on blanks", "git  status\t-s", []cmd{args("git", "status", "-s")}},
		{"keeps nothing for blank text", " \n ", nil},
		{"removes single quotes", `echo 'a "b" $c'`, []cmd{args("echo", `a "b" $c`)}},
		{"removes double quotes and keeps their escapes", `echo "a \"b\" \$c \x"`, []cmd{args("echo", `a "b" $c \x`)}},
		{"keeps an empty quoted word", `echo '' ""`, []cmd{args("echo", "", "")}},
		{"joins quoted parts of one word", `echo a'b'"c"\ d`, []cmd{args("echo", "abc d")}},
		{"escapes the next character", `echo a\;b`, []cmd{args("echo", "a;b")}},
		{"joins lines on a backslash line break", "git commit \\\n  -m x", []cmd{args("git", "commit", "-m", "x")}},
		{"joins lines inside double quotes", "echo \"a\\\nb\"", []cmd{args("echo", "ab")}},
		{
			"splits on every operator",
			"a; b && c || d | e & f\ng",
			[]cmd{args("a"), args("b"), args("c"), args("d"), args("e"), args("f"), args("g")},
		},
		{"splits a subshell", "(cd x; gh pr view 1)", []cmd{args("cd", "x"), args("gh", "pr", "view", "1")}},
		{"keeps operators inside quotes", `gh pr comment 1 -b "a; gh is fine"`, []cmd{args("gh", "pr", "comment", "1", "-b", "a; gh is fine")}},
		{"drops a comment", "git status # gh pr comment 1\ngit log", []cmd{args("git", "status"), args("git", "log")}},
		{"keeps a hash inside a word", "echo a#b '#c'", []cmd{args("echo", "a#b", "#c")}},
		{
			"reads env assignments before the command name",
			`GIT_EDITOR=x A="b c" git commit X=y`,
			[]cmd{{Args: []string{"git", "commit", "X=y"}, Env: map[string]string{"GIT_EDITOR": "x", "A": "b c"}}},
		},
		{"keeps a quoted assignment as the command name", `"A=b" cmd`, []cmd{args("A=b", "cmd")}},
		{"keeps assignments alone", "A=1", []cmd{{Env: map[string]string{"A": "1"}}}},
		{"keeps a wrapper assignment as a word", "env A=1 gh pr view", []cmd{args("env", "A=1", "gh", "pr", "view")}},
		{
			"keeps redirection targets apart from the words",
			"gh pr view 1 2>&1 >/dev/null <in >> log &> all 3< x",
			[]cmd{{Args: []string{"gh", "pr", "view", "1"}, Redirects: []string{"1", "/dev/null", "in", "log", "all", "x"}}},
		},
		{"keeps a quoted number before a redirection", `echo "2">x`, []cmd{{Args: []string{"echo", "2"}, Redirects: []string{"x"}}}},
		{
			"keeps substitutions in redirection targets as written",
			`cat < <(gh pr view) > "$(gh x)"`,
			[]cmd{{Args: []string{"cat"}, Redirects: []string{"<(gh pr view)", "$(gh x)"}}},
		},
		{"keeps a command of redirections only", "> $(gh x)", []cmd{{Redirects: []string{"$(gh x)"}}}},
		{
			"attaches a here document to its command",
			"git commit -F - <<EOF\nfix: a; b\n$(rm x)\nEOF\ngit push",
			[]cmd{{Args: []string{"git", "commit", "-F", "-"}, Stdin: "fix: a; b\n$(rm x)"}, args("git", "push")},
		},
		{
			"gives a piped here document to the command declaring it",
			"cat <<EOF | git commit -F -\nmsg\nEOF",
			[]cmd{{Args: []string{"cat"}, Stdin: "msg"}, args("git", "commit", "-F", "-")},
		},
		{
			"reads a quoted delimiter",
			"cat <<'END X'\na\nEND X\necho",
			[]cmd{{Args: []string{"cat"}, Stdin: "a"}, args("echo")},
		},
		{
			"strips tabs with <<-",
			"cat <<-EOF\n\ta\n\t\tb\n\tEOF\necho",
			[]cmd{{Args: []string{"cat"}, Stdin: "a\nb"}, args("echo")},
		},
		{
			"reads two here documents of one line in order",
			"cat <<A; cat <<B\na\nA\nb\nB",
			[]cmd{{Args: []string{"cat"}, Stdin: "a"}, {Args: []string{"cat"}, Stdin: "b"}},
		},
		{"runs an unterminated here document to the end", "cat <<EOF\na\nb", []cmd{{Args: []string{"cat"}, Stdin: "a\nb"}}},
		{
			"reads a here-string as stdin",
			`git commit -F - <<< "msg; x" && git push`,
			[]cmd{{Args: []string{"git", "commit", "-F", "-"}, Stdin: "msg; x"}, args("git", "push")},
		},
		{
			"takes the body of a cat here document substitution",
			"git commit -m \"$(cat <<'EOF'\nDon't (stop)\n\"quoted\"\nEOF\n)\" && git push",
			[]cmd{args("git", "commit", "-m", "Don't (stop)\n\"quoted\""), args("git", "push")},
		},
		{
			"keeps other substitutions as written",
			`echo "$(gh pr comment 1 -R x/r -b "hi")" $(a; b) x`,
			[]cmd{args("echo", `$(gh pr comment 1 -R x/r -b "hi")`, "$(a; b)", "x")},
		},
		{"keeps nested substitutions", "echo $(a $(b) (c))", []cmd{args("echo", "$(a $(b) (c))")}},
		{"keeps backticks as written", "echo `gh pr view; x` y", []cmd{args("echo", "`gh pr view; x`", "y")}},
		{"keeps arithmetic as written", "echo $((1 << 2)) && git push", []cmd{args("echo", "$((1 << 2))"), args("git", "push")}},
		{"keeps an arithmetic command", "((i << 2)); git push", []cmd{args("((i << 2))"), args("git", "push")}},
		{"keeps a parameter expansion", "echo ${a:-x y}", []cmd{args("echo", "${a:-x y}")}},
		{"keeps a process substitution", "diff <(a x) b", []cmd{args("diff", "<(a x)", "b")}},
		{"keeps a lone dollar", "echo $ a$", []cmd{args("echo", "$", "a$")}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shell.Commands(tc.text))
		})
	}
}
