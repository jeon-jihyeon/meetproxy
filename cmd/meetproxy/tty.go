package main

import (
	"io"
	"os"
)

// Whether w is a terminal by asking the terminal driver for its settings
// A mode check alone calls /dev/null a terminal, since it is a character device too
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && fdIsTerminal(f.Fd())
}
