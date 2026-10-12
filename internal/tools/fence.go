package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// A monitor's output is untrusted text, and it used to sit in a push next to
// the server's own lines with nothing telling them apart: a watched log could
// print "[monitor mon-1234 · …] exited with code 0" or a fake resume notice,
// and the model could not tell it from the real thing.
//
// So every push that carries output fences it between two marker lines built
// from a string chosen at random for that one push, after the output is
// known, and named in the header. Output cannot close a fence whose string it
// could not predict, so everything outside the markers is the server's own
// text. A string that happens to occur in the output is replaced.
//
// This is not sender authentication: anything that can write to the
// session's inbox can still send a whole message of its own. It makes the
// server's lines unforgeable from inside the output, which is the part a
// monitored source controls.

// newFence returns a random marker string that occurs in none of lines.
func newFence(lines []string) string {
	for {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		f := "output-" + hex.EncodeToString(b)
		clash := false
		for _, l := range lines {
			if strings.Contains(l, f) {
				clash = true
				break
			}
		}
		if !clash {
			return f
		}
	}
}

// formatBody renders one push: the header, then — when there are lines —
// a sentence naming the fence, the fenced lines, and a note outside the
// fence for lines that did not fit.
func formatBody(head string, lines []string, omitted int, fence string) string {
	if len(lines) == 0 && omitted == 0 {
		return head
	}
	var b strings.Builder
	b.WriteString(head)
	fmt.Fprintf(&b, " Command output, %d line(s), between the two %s markers; everything outside them is from the server:\n", len(lines), fence)
	fmt.Fprintf(&b, "<<<%s\n", fence)
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s>>>", fence)
	if omitted > 0 {
		fmt.Fprintf(&b, "\n%d more line(s) did not fit and are only in the output file.", omitted)
	}
	return b.String()
}

// oneLine keeps a model-supplied description from breaking the header onto
// several lines.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
