package annotate

import (
	"errors"
	"html"
	"regexp"
	"strings"
)

// TextEdit replaces part of one text node. The browser describes the node by
// the start tag that precedes it in the source and by its exact text, split
// around the part to replace.
type TextEdit struct {
	At     int    // byte offset of the nearest start tag before the text
	Before string // the node's text before the selection
	Old    string // the selected text
	After  string // the node's text after the selection
	New    string // what to put in place of Old
}

// ErrNotInSource means the text could not be pinned to exactly one place in the
// file — typically because a script wrote it, not the file. Nothing is changed.
var ErrNotInSource = errors.New("this text was not found at that place in the source file")

// segment maps a stretch of source to the text it decodes to. In a plain
// segment the two are byte-for-byte identical; an atomic one (a character
// reference, or a line ending) can only be replaced as a whole.
type segment struct {
	src, dec int // start offsets, in the run and in the decoded text
	atomic   bool
}

var charRef = regexp.MustCompile(`^&(?:#[xX][0-9a-fA-F]+|#[0-9]+|[a-zA-Z][a-zA-Z0-9]*);?`)

// decode turns a run of source text into what the browser shows for it,
// keeping track of which source bytes produced which text.
func decode(run string) (text string, segs []segment) {
	var out strings.Builder
	plain := false
	add := func(i int, atomic bool, decoded string) {
		if atomic || !plain {
			segs = append(segs, segment{src: i, dec: out.Len(), atomic: atomic})
		}
		plain = !atomic
		out.WriteString(decoded)
	}
	for i := 0; i < len(run); {
		switch c := run[i]; {
		case c == '&':
			if ref := charRef.FindString(run[i:]); ref != "" {
				if decoded := html.UnescapeString(ref); decoded != ref {
					add(i, true, decoded)
					i += len(ref)
					continue
				}
			}
			add(i, false, "&")
			i++
		case c == '\r': // the parser turns CRLF and lone CR into LF
			width := 1
			if i+1 < len(run) && run[i+1] == '\n' {
				width = 2
			}
			add(i, true, "\n")
			i += width
		default:
			add(i, false, run[i:i+1])
			i++
		}
	}
	segs = append(segs, segment{src: len(run), dec: out.Len()}) // sentinel
	return out.String(), segs
}

// sourceOffset maps an offset in decoded text back to the run, or returns -1 if
// it falls inside something that cannot be split.
func sourceOffset(segs []segment, dec int) int {
	for i := 0; i+1 < len(segs); i++ {
		s, next := segs[i], segs[i+1]
		if dec == s.dec {
			return s.src
		}
		if dec < next.dec {
			if s.atomic {
				return -1
			}
			return s.src + (dec - s.dec)
		}
	}
	if last := segs[len(segs)-1]; dec == last.dec {
		return last.src
	}
	return -1
}

var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// ReplaceText applies e to src and returns the new source and the line the
// change is on. It only succeeds when exactly one run of text between the
// start tag at e.At and the next start tag reads Before+Old+After; otherwise
// src is returned unchanged with ErrNotInSource.
func ReplaceText(src string, e TextEdit) (out string, line int, err error) {
	tokens, textEnd := scan(src)
	first := -1
	for i, t := range tokens {
		if t.marked && t.start == e.At {
			first = i
			break
		}
	}
	if first == -1 || e.Old == "" {
		return src, 0, ErrNotInSource
	}

	want := e.Before + e.Old + e.After
	from, to := -1, -1 // the source range to replace
	for i := first; i < len(tokens); i++ {
		runStart, runEnd := tokens[i].end, textEnd
		if i+1 < len(tokens) {
			runEnd = tokens[i+1].start
		}
		if runEnd > runStart {
			if text, segs := decode(src[runStart:runEnd]); text == want {
				if from != -1 {
					return src, 0, ErrNotInSource // the same text twice: cannot tell which
				}
				a := sourceOffset(segs, len(e.Before))
				b := sourceOffset(segs, len(e.Before)+len(e.Old))
				if a == -1 || b == -1 {
					return src, 0, ErrNotInSource
				}
				from, to = runStart+a, runStart+b
			}
		}
		if i+1 < len(tokens) && tokens[i+1].marked {
			break // text after the next start tag belongs to that tag
		}
	}
	if from == -1 {
		return src, 0, ErrNotInSource
	}
	return src[:from] + textEscaper.Replace(e.New) + src[to:], 1 + strings.Count(src[:from], "\n"), nil
}
