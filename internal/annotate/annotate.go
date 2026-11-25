// Package annotate prepares an HTML document for review without touching the
// file on disk: every start tag gets attributes saying where it sits in the
// source (so a comment can be reported as file:line, and an edit can be
// written back to the right place), and the overlay script is added.
package annotate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	// LineAttr carries the source line of a start tag.
	LineAttr = "data-inliner-line"
	// AtAttr carries the byte offset of a start tag in the source.
	AtAttr = "data-inliner-at"
	// ClientPath is the URL the overlay script is served at.
	ClientPath = "/__inliner/client.js"
)

// Tags that never render anything a reviewer could point at.
var skipTags = map[string]bool{
	"html": true, "head": true, "meta": true, "link": true, "title": true,
	"script": true, "style": true, "base": true, "noscript": true,
}

// Tags whose content is raw text: anything that looks like a tag inside is not one.
var rawTextTags = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
	"xmp": true, "iframe": true, "noembed": true, "noframes": true,
}

func isNameStart(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isNameEnd(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f'
}

// lowerASCII lower-cases A–Z only, so byte offsets stay valid for src.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// token is a piece of markup: src[start:end]. Whatever lies between two
// consecutive tokens is text.
type token struct {
	start, end int
	nameEnd    int  // start tags: where the tag name ends
	marked     bool // a start tag that gets the source attributes
}

// scan splits src into markup tokens. textEnd is where the text after the last
// token ends: len(src), or the start of a tag that is never closed, which is
// left alone together with everything after it.
func scan(src string) (tokens []token, textEnd int) {
	n := len(src)
	lower := lowerASCII(src)
	// past returns the index just after the next occurrence of end, or n.
	past := func(from int, end string) int {
		if from >= n {
			return n
		}
		if i := strings.Index(src[from:], end); i != -1 {
			return from + i + len(end)
		}
		return n
	}

	i := 0
	for i < n {
		rel := strings.IndexByte(src[i:], '<')
		if rel == -1 || i+rel+1 >= n {
			break
		}
		lt := i + rel
		next := src[lt+1]

		if strings.HasPrefix(src[lt:], "<!--") {
			i = past(lt+4, "-->")
			tokens = append(tokens, token{start: lt, end: i})
			continue
		}
		if next == '!' || next == '?' || next == '/' {
			i = past(lt+2, ">")
			tokens = append(tokens, token{start: lt, end: i})
			continue
		}
		if !isNameStart(next) {
			i = lt + 1 // a stray "<" is text
			continue
		}

		nameEnd := lt + 1
		for nameEnd < n && !isNameEnd(src[nameEnd]) {
			nameEnd++
		}
		name := lower[lt+1 : nameEnd]

		// Find the end of the tag; ">" inside a quoted attribute value does not count.
		tagEnd := nameEnd
		var quote byte
		for tagEnd < n {
			c := src[tagEnd]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '"' || c == '\'' {
				quote = c
			} else if c == '>' {
				break
			}
			tagEnd++
		}
		if tagEnd >= n {
			return tokens, lt // unterminated tag
		}
		i = tagEnd + 1
		tokens = append(tokens, token{start: lt, end: i, nameEnd: nameEnd, marked: !skipTags[name]})

		if rawTextTags[name] {
			// The raw content is not text a reviewer can edit: cover it with a token.
			if end := rawTextEnd(lower, i, name); end > i {
				tokens = append(tokens, token{start: i, end: end})
				i = end
			}
		}
	}
	return tokens, n
}

// rawTextEnd returns the index of the closing tag for a raw-text element
// opened before from, or len(lower) if it is never closed.
func rawTextEnd(lower string, from int, name string) int {
	closing := "</" + name
	for from < len(lower) {
		rel := strings.Index(lower[from:], closing)
		if rel == -1 {
			break
		}
		at := from + rel
		after := at + len(closing)
		if after < len(lower) && isNameEnd(lower[after]) {
			return at
		}
		from = after
	}
	return len(lower)
}

// Lines adds the source line and byte offset to each start tag. It never adds
// or removes newlines, so line numbers keep matching the file on disk.
func Lines(src string) string {
	tokens, _ := scan(src)
	var out strings.Builder
	out.Grow(len(src) + len(src)/3)
	copied := 0  // src[:copied] is already in out
	line := 1    // line number at src[counted]
	counted := 0 // newlines in src[:counted] are included in line
	for _, t := range tokens {
		if !t.marked {
			continue
		}
		line += strings.Count(src[counted:t.start], "\n")
		counted = t.start
		out.WriteString(src[copied:t.nameEnd])
		out.WriteString(" " + LineAttr + `="` + strconv.Itoa(line) + `" ` + AtAttr + `="` + strconv.Itoa(t.start) + `"`)
		copied = t.nameEnd
	}
	out.WriteString(src[copied:])
	return out.String()
}

// Rev identifies one version of a file's content. Offsets handed to the
// browser are only valid for the version they were computed from.
func Rev(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:8])
}

var attrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;")

// InjectClient appends the overlay script, just before </body> when there is one.
func InjectClient(html, file, rev string) string {
	tag := `<script src="` + ClientPath + `" data-inliner-file="` + attrEscaper.Replace(file) + `" data-inliner-rev="` + rev + `" defer></script>`
	idx := strings.LastIndex(lowerASCII(html), "</body")
	if idx == -1 {
		return html + "\n" + tag + "\n"
	}
	return html[:idx] + tag + "\n" + html[idx:]
}

// Prepare annotates the source and injects the overlay script.
func Prepare(src, file string) string {
	return InjectClient(Lines(src), file, Rev(src))
}
