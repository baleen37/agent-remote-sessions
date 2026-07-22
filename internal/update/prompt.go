package update

import (
	"fmt"
	"io"
)

// Confirm asks whether to install the newer release. The prompt is
// printed before makeRaw so newlines render in cooked mode; raw mode
// covers only the single-key read.
func Confirm(input io.Reader, output io.Writer, current, latest string, makeRaw func() (restore func(), err error)) bool {
	fmt.Fprintf(output, "ars v%s available (current v%s)\n", latest, current)
	fmt.Fprintln(output, "Enter: update now    any other key: skip")
	restore, err := makeRaw()
	if err != nil {
		return false
	}
	defer restore()
	var key [1]byte
	if _, err := input.Read(key[:]); err != nil {
		return false
	}
	return key[0] == '\r' || key[0] == '\n'
}
