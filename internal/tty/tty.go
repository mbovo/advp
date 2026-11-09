// Package tty switches a terminal into cbreak mode (keys are delivered one at
// a time without echo, while Ctrl-C and output processing keep working) and
// decodes key presses for the live view.
package tty

// Key is a decoded key press.
type Key int

// Keys understood by the live view.
const (
	KeyNone Key = iota
	KeyQuit
	KeyPause
	KeyView
	KeyRedraw
)

// Decode maps one read from the terminal to keys. A lone ESC quits; escape
// sequences such as arrow keys (ESC [ A) are ignored.
func Decode(b []byte) []Key {
	var keys []Key
	for i := 0; i < len(b); i++ {
		switch c := b[i]; c {
		case 0x1b:
			if i == len(b)-1 {
				keys = append(keys, KeyQuit)
			}
			return keys // escape sequence: drop the rest of this read
		case 'q', 'Q', 0x03:
			keys = append(keys, KeyQuit)
		case 'p', 'P', ' ':
			keys = append(keys, KeyPause)
		case 'v', 'V', '\t':
			keys = append(keys, KeyView)
		case 'r', 'R', 0x0c: // Ctrl-L
			keys = append(keys, KeyRedraw)
		}
	}
	return keys
}
