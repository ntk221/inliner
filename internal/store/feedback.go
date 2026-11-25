package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RenderOptions controls Render.
type RenderOptions struct {
	Root            string // project root; files are read from here to check line numbers
	CLI             string // command that runs this program, for the "resolve" hint
	IncludeResolved bool
}

func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func code(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

func isNameChar(c byte) bool {
	return c == '_' || c == ':' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// verifiedLine returns the anchor's line number if that line of the current
// file still opens the same tag, and 0 otherwise. A stored line number is only
// as fresh as the last time the page was open in the browser; a missing line
// number is better than a wrong one.
func verifiedLine(a Anchor, lines []string) int {
	if a.Line < 1 || a.Line > len(lines) || a.LineTag == "" {
		return 0
	}
	text := strings.ToLower(lines[a.Line-1])
	open := "<" + strings.ToLower(a.LineTag)
	for from := 0; ; {
		i := strings.Index(text[from:], open)
		if i == -1 {
			return 0
		}
		end := from + i + len(open)
		if end == len(text) || !isNameChar(text[end]) {
			return a.Line
		}
		from = end
	}
}

func describe(w *strings.Builder, c Comment, line int) {
	a := c.Anchor
	where := c.File
	if line > 0 {
		where = fmt.Sprintf("%s:%d", c.File, line)
	}
	fmt.Fprintf(w, "### #%d · %s · %s\n\n", c.ID, code(where), c.Kind)

	switch c.Kind {
	case KindText:
		fmt.Fprintf(w, "- Selected text: \"%s\"\n", clip(a.Quote, 400))
		if len([]rune(a.Quote)) < 80 && (a.Prefix != "" || a.Suffix != "") {
			fmt.Fprintf(w, "- In context: \"…%s[[%s]]%s…\"\n", a.Prefix, a.Quote, a.Suffix)
		}
		if a.Tag != "" {
			fmt.Fprintf(w, "- Inside: %s", code(a.Tag))
			if a.Selector != "" {
				fmt.Fprintf(w, " (selector %s)", code(a.Selector))
			}
			w.WriteString("\n")
		}
	case KindElement:
		if a.Tag != "" {
			fmt.Fprintf(w, "- Element: %s\n", code(a.Tag))
		}
		if a.Selector != "" {
			fmt.Fprintf(w, "- Selector: %s\n", code(a.Selector))
		}
		if a.Text != "" {
			fmt.Fprintf(w, "- Element text: \"%s\"\n", clip(a.Text, 200))
		}
	default:
		w.WriteString("- Applies to the page as a whole.\n")
	}

	if c.Kind != KindPage {
		if !c.Anchored {
			w.WriteString("- Note: this location was not found in the page as last rendered — it may already have been changed or removed.\n")
		} else if line == 0 {
			w.WriteString("- Note: line number unknown; locate it by the quoted text or selector.\n")
		}
	}
	if c.Status == StatusResolved {
		w.WriteString("- Status: resolved")
		if c.Resolution != nil && c.Resolution.Note != "" {
			w.WriteString(" — " + c.Resolution.Note)
		}
		w.WriteString("\n")
	}

	w.WriteString("\n")
	for _, l := range strings.Split(c.Body, "\n") {
		if l == "" {
			w.WriteString(">\n")
		} else {
			w.WriteString("> " + l + "\n")
		}
	}
	w.WriteString("\n")
}

// describeEdits lists what the reviewer changed in the files themselves.
func describeEdits(w *strings.Builder, edits []Edit) {
	if len(edits) == 0 {
		return
	}
	if len(edits) > 20 {
		edits = edits[len(edits)-20:]
	}
	w.WriteString("## Edited directly by the reviewer\n\n")
	w.WriteString("The reviewer already made these changes in the source files. Keep them — do not revert them.\n\n")
	for _, e := range edits {
		replacement := "(deleted)"
		if e.New != "" {
			replacement = `"` + clip(e.New, 200) + `"`
		}
		fmt.Fprintf(w, "- %s (line %d at the time): \"%s\" → %s\n", code(e.File), e.Line, clip(e.Old, 200), replacement)
	}
	w.WriteString("\n")
}

// Render writes review comments as Markdown addressed to the AI that wrote the
// HTML, followed by the edits the reviewer made themselves.
func Render(comments []Comment, edits []Edit, opts RenderOptions) string {
	var shown []Comment
	open := 0
	for _, c := range comments {
		if c.Status == StatusOpen {
			open++
		} else if !opts.IncludeResolved {
			continue
		}
		shown = append(shown, c)
	}

	var w strings.Builder
	w.WriteString("# Inline review comments\n\n")
	if len(shown) == 0 {
		w.WriteString("No open comments.\n\n")
		describeEdits(&w, edits)
		return strings.TrimRight(w.String(), "\n") + "\n"
	}

	cli := opts.CLI
	if cli == "" {
		cli = "inliner"
	}
	fmt.Fprintf(&w, `A human reviewer left these comments on the rendered HTML pages, using inliner.
Each one points at a place in a source file: the heading gives `+"`file:line`"+` (the opening
tag of the nearest element), followed by what was selected and the comment itself (quoted).

- Root: %s (file paths are relative to it)
- Open comments: %d

Apply each requested change to the source file. After addressing a comment, mark it resolved:

    %s resolve <id> -m "what you changed"

`, code(opts.Root), open, cli)

	byFile := map[string][]Comment{}
	var files []string
	for _, c := range shown {
		if _, seen := byFile[c.File]; !seen {
			files = append(files, c.File)
		}
		byFile[c.File] = append(byFile[c.File], c)
	}
	sort.Strings(files)

	for _, file := range files {
		// The file may have been moved or deleted; its comments are still worth showing.
		var lines []string
		raw, err := os.ReadFile(filepath.Join(opts.Root, filepath.FromSlash(file)))
		if err == nil {
			lines = strings.Split(string(raw), "\n")
		}

		type item struct {
			c    Comment
			line int
		}
		items := make([]item, 0, len(byFile[file]))
		for _, c := range byFile[file] {
			line := 0
			// No line for a comment whose target is gone: where it used to be would only mislead.
			if c.Kind != KindPage && c.Anchored {
				line = verifiedLine(c.Anchor, lines)
			}
			items = append(items, item{c, line})
		}
		// Document order, with comments that have no line last.
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if (a.line == 0) != (b.line == 0) {
				return b.line == 0
			}
			if a.line != b.line {
				return a.line < b.line
			}
			return a.c.ID < b.c.ID
		})

		fmt.Fprintf(&w, "## %s\n\n", file)
		if err != nil {
			w.WriteString("_This file no longer exists at this path._\n\n")
		}
		for _, it := range items {
			describe(&w, it.c, it.line)
		}
	}
	describeEdits(&w, edits)
	return strings.TrimRight(w.String(), "\n") + "\n"
}
