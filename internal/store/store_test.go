package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const page = "<html>\n<body>\n<h1>Title</h1>\n<p class=\"lead\">Hello world</p>\n</body>\n</html>\n"

func tempStore(t *testing.T, files map[string]string) *Store {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return New(root)
}

func textComment() NewComment {
	return NewComment{
		File: "page.html",
		Kind: KindText,
		Body: "Say more here",
		Anchor: Anchor{
			Quote: "Hello", Suffix: " world", Selector: "body > p", Tag: `<p class="lead">`, Line: 4, LineTag: "p",
		},
	}
}

func mustAdd(t *testing.T, s *Store, in NewComment) Comment {
	t.Helper()
	c, err := s.Add(in)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return c
}

func mustFeedback(t *testing.T, s *Store, file string, includeResolved bool) string {
	t.Helper()
	md, err := s.Feedback(file, includeResolved)
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	return md
}

func wantMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("no match for %q in:\n%s", pattern, text)
	}
}

func wantNoMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("unexpected match for %q in:\n%s", pattern, text)
	}
}

func ids(comments []Comment) []int {
	out := []int{}
	for _, c := range comments {
		out = append(out, c.ID)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNormalizeFile(t *testing.T) {
	good := map[string]string{
		"a/b.html":      "a/b.html",
		"/a//b.html":    "a/b.html",
		`a\b.html`:      "a/b.html",
		"a/../b.html":   "b.html",
		"./docs/x.html": "docs/x.html",
	}
	for in, want := range good {
		if got, ok := NormalizeFile(in); !ok || got != want {
			t.Errorf("NormalizeFile(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"../x.html", "a/../../x", "..", "", ".", "/", "a\x00b"} {
		if got, ok := NormalizeFile(bad); ok {
			t.Errorf("NormalizeFile(%q) = %q, want rejection", bad, got)
		}
	}
}

func TestAddWritesEverything(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	a := mustAdd(t, s, textComment())
	b := mustAdd(t, s, NewComment{File: "page.html", Kind: KindPage, Body: "  Darker theme  ", Anchor: Anchor{Quote: "ignored"}})
	if a.ID != 1 || b.ID != 2 {
		t.Errorf("ids = %d, %d", a.ID, b.ID)
	}
	if b.Body != "Darker theme" || b.Anchor != (Anchor{}) {
		t.Errorf("page comment not cleaned: %+v", b)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Dir, ".gitignore")); string(got) != "*\n" {
		t.Errorf(".gitignore = %q", got)
	}

	raw, err := os.ReadFile(s.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tag": "<p class=\"lead\">"`) {
		t.Errorf("tags should be stored readably, got:\n%s", raw)
	}
	var stored struct{ Comments []Comment }
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored.Comments) != 2 {
		t.Fatalf("comments.json: %v, %d comments", err, len(stored.Comments))
	}

	md, err := os.ReadFile(s.FeedbackFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{
		`Open comments: 2`,
		"### #1 · `page\\.html:4` · text",
		`- Selected text: "Hello"`,
		`- In context: "…\[\[Hello\]\] world…"`,
		"- Inside: `<p class=\"lead\">` \\(selector `body > p`\\)",
		`> Say more here`,
		"### #2 · `page\\.html` · page",
		`resolve <id> -m`,
	} {
		wantMatch(t, string(md), pattern)
	}
}

func TestAddRejectsInvalid(t *testing.T) {
	s := tempStore(t, nil)
	cases := map[string]func(c *NewComment){
		"empty body": func(c *NewComment) { c.Body = "   " },
		"bad kind":   func(c *NewComment) { c.Kind = "nope" },
		"bad file":   func(c *NewComment) { c.File = "../etc/passwd" },
	}
	for name, mutate := range cases {
		c := textComment()
		mutate(&c)
		_, err := s.Add(c)
		var se *Error
		if !errors.As(err, &se) || se.Status != 400 {
			t.Errorf("%s: err = %v, want a 400 Error", name, err)
		}
	}
	if _, err := os.Stat(s.File); !os.IsNotExist(err) {
		t.Errorf("nothing should have been written")
	}
}

func TestIDsAreNotReused(t *testing.T) {
	s := tempStore(t, nil)
	mustAdd(t, s, textComment())
	mustAdd(t, s, textComment())
	removed, err := s.Remove([]int{2, 9})
	if err != nil || !equalInts(ids(removed), []int{2}) {
		t.Fatalf("Remove = %v, %v", ids(removed), err)
	}
	if c := mustAdd(t, s, textComment()); c.ID != 3 {
		t.Errorf("next id = %d, want 3", c.ID)
	}
}

func TestResolveReopenClear(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	mustAdd(t, s, textComment())
	second := textComment()
	second.Body = "Second"
	mustAdd(t, s, second)

	changed, missing, err := s.SetStatus([]int{1, 7}, StatusResolved, "cli", "Rewrote it")
	if err != nil || !equalInts(ids(changed), []int{1}) || !equalInts(missing, []int{7}) {
		t.Fatalf("SetStatus = %v, %v, %v", ids(changed), missing, err)
	}
	list, _ := s.List("")
	if r := list[0].Resolution; r == nil || *r != (Resolution{By: "cli", Note: "Rewrote it"}) {
		t.Errorf("resolution = %+v", r)
	}
	wantNoMatch(t, mustFeedback(t, s, "", false), `### #1 `)
	wantMatch(t, mustFeedback(t, s, "", true), `### #1 [\s\S]*Status: resolved — Rewrote it`)

	if _, _, err := s.SetStatus([]int{1}, StatusOpen, "cli", ""); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List("")
	if list[0].Resolution != nil || list[0].ResolvedAt != "" {
		t.Errorf("reopen should drop the resolution: %+v", list[0])
	}
	wantMatch(t, mustFeedback(t, s, "", false), `### #1 `)

	resolved := StatusResolved
	if c, err := s.Update(2, Patch{Status: &resolved}); err != nil || c == nil || c.Resolution.By != "reviewer" {
		t.Fatalf("Update = %+v, %v", c, err)
	}
	bogus := "bogus"
	if _, err := s.Update(1, Patch{Status: &bogus}); err == nil {
		t.Errorf("bogus status accepted")
	}
	if c, err := s.Update(99, Patch{Status: &resolved}); c != nil || err != nil {
		t.Errorf("Update of a missing comment = %+v, %v", c, err)
	}

	if n, _ := s.Clear(false); n != 1 {
		t.Errorf("Clear removed %d, want 1", n)
	}
	if list, _ = s.List(""); !equalInts(ids(list), []int{1}) {
		t.Errorf("left after Clear: %v", ids(list))
	}
	if n, _ := s.Clear(true); n != 1 {
		t.Errorf("Clear(all) removed %d, want 1", n)
	}
	if md, _ := os.ReadFile(s.FeedbackFile); string(md) != "# Inline review comments\n\nNo open comments.\n" {
		t.Errorf("feedback.md = %q", md)
	}
}

func TestStaleLineIsDropped(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	mustAdd(t, s, textComment())
	shifted := strings.Replace(page, "<h1>", "<nav></nav>\n<h1>", 1)
	if err := os.WriteFile(filepath.Join(s.Root, "page.html"), []byte(shifted), 0o644); err != nil {
		t.Fatal(err)
	}
	md := mustFeedback(t, s, "", false)
	wantMatch(t, md, "### #1 · `page\\.html` · text")
	wantMatch(t, md, `line number unknown`)
}

func TestVerifiedLine(t *testing.T) {
	lines := []string{`<P class="x">`, `<pre>code</pre>`, `<my-card>`, `text only`}
	tests := []struct {
		a    Anchor
		want int
	}{
		{Anchor{Line: 1, LineTag: "p"}, 1},
		{Anchor{Line: 2, LineTag: "p"}, 0}, // <pre> is not <p>
		{Anchor{Line: 2, LineTag: "pre"}, 2},
		{Anchor{Line: 3, LineTag: "my-card"}, 3},
		{Anchor{Line: 3, LineTag: "my"}, 0},
		{Anchor{Line: 4, LineTag: "p"}, 0},
		{Anchor{Line: 9, LineTag: "p"}, 0},
		{Anchor{Line: 1}, 0},
		{Anchor{LineTag: "p"}, 0},
	}
	for _, tt := range tests {
		if got := verifiedLine(tt.a, lines); got != tt.want {
			t.Errorf("verifiedLine(%+v) = %d, want %d", tt.a, got, tt.want)
		}
	}
}

func TestSyncAnchors(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": strings.Replace(page, "<h1>", "<nav></nav>\n<h1>", 1)})
	created := mustAdd(t, s, textComment())
	no := false

	moved := []AnchorUpdate{{ID: 1, Line: 5, LineTag: "p"}}
	if changed, _ := s.SyncAnchors("page.html", moved); !changed {
		t.Errorf("a move should count as a change")
	}
	if changed, _ := s.SyncAnchors("page.html", moved); changed {
		t.Errorf("the same report twice should not")
	}
	if changed, _ := s.SyncAnchors("other.html", []AnchorUpdate{{ID: 1, Anchored: &no}}); changed {
		t.Errorf("a report for another file should be ignored")
	}
	wantMatch(t, mustFeedback(t, s, "", false), "### #1 · `page\\.html:5` · text")
	if list, _ := s.List(""); list[0].UpdatedAt != created.UpdatedAt {
		t.Errorf("SyncAnchors must not touch UpdatedAt")
	}

	// The text around the quote was rewritten: the feedback should show it as it is now.
	prefix, suffix := "Oh, ", " there"
	if changed, _ := s.SyncAnchors("page.html", []AnchorUpdate{{ID: 1, Line: 5, LineTag: "p", Prefix: &prefix, Suffix: &suffix}}); !changed {
		t.Errorf("new context should count as a change")
	}
	if changed, _ := s.SyncAnchors("page.html", []AnchorUpdate{{ID: 1, Line: 5, LineTag: "p", Prefix: &prefix, Suffix: &suffix}}); changed {
		t.Errorf("the same context twice should not")
	}
	wantMatch(t, mustFeedback(t, s, "", false), `In context: "…Oh, \[\[Hello\]\] there…"`)

	if changed, _ := s.SyncAnchors("page.html", []AnchorUpdate{{ID: 1, Anchored: &no}}); !changed {
		t.Errorf("losing the anchor should count as a change")
	}
	md := mustFeedback(t, s, "", false)
	wantMatch(t, md, "### #1 · `page\\.html` · text")
	wantMatch(t, md, `was not found in the page as last rendered`)
}

func TestFeedbackPerFileAndMissingFile(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	mustAdd(t, s, textComment())
	gone := textComment()
	gone.File, gone.Body = "gone.html", "Other page"
	mustAdd(t, s, gone)

	wantNoMatch(t, mustFeedback(t, s, "page.html", false), `gone\.html`)
	wantMatch(t, mustFeedback(t, s, "", false), `## gone\.html\n\n_This file no longer exists at this path\._`)
}

func TestFeedbackOrder(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	mustAdd(t, s, NewComment{File: "page.html", Kind: KindPage, Body: "whole page"})
	mustAdd(t, s, textComment()) // line 4
	h1 := NewComment{File: "page.html", Kind: KindElement, Body: "bigger", Anchor: Anchor{Tag: "<h1>", Selector: "body > h1", Text: "Title", Line: 3, LineTag: "h1"}}
	mustAdd(t, s, h1)
	md := mustFeedback(t, s, "", false)
	order := regexp.MustCompile(`### #(\d)`).FindAllStringSubmatch(md, -1)
	if len(order) != 3 || order[0][1] != "3" || order[1][1] != "2" || order[2][1] != "1" {
		t.Errorf("want document order #3, #2, then the page comment #1; got %v\n%s", order, md)
	}
	wantMatch(t, md, "- Element: `<h1>`\n- Selector: `body > h1`\n- Element text: \"Title\"")
}

func TestCorruptStoreIsReportedNotOverwritten(t *testing.T) {
	s := tempStore(t, nil)
	mustAdd(t, s, textComment())
	if err := os.WriteFile(s.File, []byte("{ nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Add(textComment())
	var se *Error
	if !errors.As(err, &se) || se.Status != 500 || !strings.Contains(se.Msg, "not valid JSON") {
		t.Errorf("err = %v", err)
	}
	if raw, _ := os.ReadFile(s.File); string(raw) != "{ nope" {
		t.Errorf("corrupt file was overwritten: %q", raw)
	}
}

func TestDirectEditsAreReportedUntilCleared(t *testing.T) {
	s := tempStore(t, map[string]string{"page.html": page})
	if err := s.AddEdit(Edit{File: "page.html", Line: 4, Old: "Hello", New: "Hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEdit(Edit{File: "other.html", Line: 9, Old: "gone", New: ""}); err != nil {
		t.Fatal(err)
	}

	// With nothing else to say, the edits are still worth telling the AI about.
	md, _ := os.ReadFile(s.FeedbackFile)
	wantMatch(t, string(md), `No open comments\.\n\n## Edited directly by the reviewer`)
	wantMatch(t, string(md), "- `page\\.html` \\(line 4 at the time\\): \"Hello\" → \"Hi\"")
	wantMatch(t, string(md), "- `other\\.html` \\(line 9 at the time\\): \"gone\" → \\(deleted\\)")

	mustAdd(t, s, textComment())
	one := mustFeedback(t, s, "page.html", false)
	wantMatch(t, one, `### #1 [\s\S]*## Edited directly by the reviewer[\s\S]*"Hello" → "Hi"`)
	wantNoMatch(t, one, `other\.html`)

	for i := 0; i < maxEdits+5; i++ {
		if err := s.AddEdit(Edit{File: "page.html", Line: i + 1, Old: "a", New: "b"}); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := s.read(); len(d.Edits) != maxEdits || d.Edits[len(d.Edits)-1].Line != maxEdits+5 {
		t.Errorf("the log should keep the %d most recent edits, has %d", maxEdits, len(d.Edits))
	}

	if n, err := s.Clear(false); n != 0 || err != nil {
		t.Errorf("Clear = %d, %v", n, err)
	}
	wantNoMatch(t, mustFeedback(t, s, "", false), `Edited directly`)
}
