// Package annotate prepares an HTML document for review without touching the
// file on disk: every start tag gets a data-inliner-line attribute carrying its
// source line (so a comment can be reported as file:line), and the overlay
// script is added.
package annotate

import (
	"strconv"
	"strings"
)

const (
	// LineAttr is the attribute the overlay reads source lines from.
	LineAttr = "data-inliner-line"
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

// Lines adds data-inliner-line="N" to each start tag. It never adds or removes
// newlines, so line numbers keep matching the file on disk.
func Lines(src string) string {
	n := len(src)
	lower := lowerASCII(src)
	var out strings.Builder
	out.Grow(n + n/4)
	copied := 0  // src[:copied] is already in out
	line := 1    // line number at src[counted]
	counted := 0 // newlines in src[:counted] are included in line
	lineAt := func(pos int) int {
		line += strings.Count(src[counted:pos], "\n")
		counted = pos
		return line
	}
	// skipPast returns the index just after the next occurrence of end, or n.
	skipPast := func(from int, end string) int {
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
			i = skipPast(lt+4, "-->")
			continue
		}
		if next == '!' || next == '?' || next == '/' {
			i = skipPast(lt+2, ">")
			continue
		}
		if !isNameStart(next) {
			i = lt + 1
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
			break // unterminated tag: leave the rest alone
		}

		if !skipTags[name] {
			out.WriteString(src[copied:nameEnd])
			out.WriteString(" " + LineAttr + `="` + strconv.Itoa(lineAt(lt)) + `"`)
			copied = nameEnd
		}
		i = tagEnd + 1

		if rawTextTags[name] {
			i = rawTextEnd(lower, i, name)
		}
	}
	out.WriteString(src[copied:])
	return out.String()
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

var attrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;")

// InjectClient appends the overlay script, just before </body> when there is one.
func InjectClient(html, file string) string {
	tag := `<script src="` + ClientPath + `" data-inliner-file="` + attrEscaper.Replace(file) + `" defer></script>`
	idx := strings.LastIndex(lowerASCII(html), "</body")
	if idx == -1 {
		return html + "\n" + tag + "\n"
	}
	return html[:idx] + tag + "\n" + html[idx:]
}

// Prepare annotates lines and injects the overlay script.
func Prepare(src, file string) string {
	return InjectClient(Lines(src), file)
}
