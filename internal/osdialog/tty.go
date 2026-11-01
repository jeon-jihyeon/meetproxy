package osdialog

import (
	"io"
	"os"
)

// Character device metadata alone does not prove that terminal-driver settings are available
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && fdIsTerminal(f.Fd())
}
