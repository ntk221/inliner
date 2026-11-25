package annotate

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var markedTag = regexp.MustCompile(`<([a-zA-Z][^\s/>]*) data-inliner-line="(\d+)" data-inliner-at="(\d+)"`)

// checkOffsets verifies that every offset written by Lines points at the same
// tag in the source, on the stated line, and returns the output without them.
func checkOffsets(t *testing.T, src, out string) string {
	t.Helper()
	for _, m := range markedTag.FindAllStringSubmatch(out, -1) {
		name := m[1]
		line, _ := strconv.Atoi(m[2])
		at, _ := strconv.Atoi(m[3])
		if at >= len(src) || !strings.HasPrefix(src[at:], "<"+name) {
			t.Errorf("offset %d does not point at <%s in the source", at, name)
			continue
		}
		if got := 1 + strings.Count(src[:at], "\n"); got != line {
			t.Errorf("<%s at %d is on line %d, marked as %d", name, at, got, line)
		}
	}
	return regexp.MustCompile(` data-inliner-at="\d+"`).ReplaceAllString(out, "")
}

func TestLines(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"marks each start tag with its source line",
			"<!doctype html>\n<html>\n<body>\n  <h1>Title</h1>\n  <p class=\"a\">One\n  two</p>\n</body>\n</html>\n",
			"<!doctype html>\n<html>\n<body data-inliner-line=\"3\">\n  <h1 data-inliner-line=\"4\">Title</h1>\n  <p data-inliner-line=\"5\" class=\"a\">One\n  two</p>\n</body>\n</html>\n",
		},
		{
			"leaves non-rendering tags alone",
			`<html><head><meta charset="utf-8"><title>T</title><link rel="x"></head><body><br/></body></html>`,
			`<html><head><meta charset="utf-8"><title>T</title><link rel="x"></head><body data-inliner-line="1"><br data-inliner-line="1"/></body></html>`,
		},
		{
			"ignores markup inside comments, scripts, styles and textareas",
			"<!-- <p>no</p> -->\n<script>const s = \"<div>no</div>\";</script>\n<STYLE>a > b { }</STYLE>\n<textarea><b>no</b></textarea>\n<p>yes</p>",
			"<!-- <p>no</p> -->\n<script>const s = \"<div>no</div>\";</script>\n<STYLE>a > b { }</STYLE>\n<textarea data-inliner-line=\"4\"><b>no</b></textarea>\n<p data-inliner-line=\"5\">yes</p>",
		},
		{
			"a script is closed by </script>, not by </scripted",
			"<script>x = '</scripted>';\n</SCRIPT >\n<p>a</p>",
			"<script>x = '</scripted>';\n</SCRIPT >\n<p data-inliner-line=\"3\">a</p>",
		},
		{
			`a ">" inside a quoted attribute does not end the tag`,
			`<p title="a > b">x</p><i>y</i>`,
			`<p data-inliner-line="1" title="a > b">x</p><i data-inliner-line="1">y</i>`,
		},
		{"stray < is left alone", "a < b and 1<2 and <", "a < b and 1<2 and <"},
		{"unterminated tag is left alone", `<p>ok</p><div class="x`, `<p data-inliner-line="1">ok</p><div class="x`},
		{"unterminated comment", "<p>a</p><!-- <b>", `<p data-inliner-line="1">a</p><!-- <b>`},
		{"multi-byte text keeps offsets right", "<p>日本語İ</p>\n<b>太字</b>", "<p data-inliner-line=\"1\">日本語İ</p>\n<b data-inliner-line=\"2\">太字</b>"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Lines(tt.src)
			if got := checkOffsets(t, tt.src, out); got != tt.want {
				t.Errorf("Lines:\n got: %q\nwant: %q", got, tt.want)
			}
			if strings.Count(out, "\n") != strings.Count(tt.src, "\n") {
				t.Errorf("line count changed")
			}
		})
	}
}

func TestInjectClient(t *testing.T) {
	const tag = `<script src="/__inliner/client.js" data-inliner-file="a/b.html" data-inliner-rev="abc" defer></script>`
	if got, want := InjectClient("<body><p>x</p></BODY></html>", "a/b.html", "abc"), "<body><p>x</p>"+tag+"\n</BODY></html>"; got != want {
		t.Errorf("before </body>:\n got: %q\nwant: %q", got, want)
	}
	if got, want := InjectClient("<p>x</p>", "a/b.html", "abc"), "<p>x</p>\n"+tag+"\n"; got != want {
		t.Errorf("no body:\n got: %q\nwant: %q", got, want)
	}
	if got := InjectClient("", `we"ird<.html`, "abc"); !strings.Contains(got, `data-inliner-file="we&quot;ird&lt;.html"`) {
		t.Errorf("file name not escaped: %q", got)
	}
}

func TestPrepare(t *testing.T) {
	src := "<body>\n<p>x</p>\n</body>"
	got := Prepare(src, "p.html")
	want := "<body data-inliner-line=\"1\" data-inliner-at=\"0\">\n<p data-inliner-line=\"2\" data-inliner-at=\"7\">x</p>\n" +
		`<script src="/__inliner/client.js" data-inliner-file="p.html" data-inliner-rev="` + Rev(src) + `" defer></script>` + "\n</body>"
	if got != want {
		t.Errorf("Prepare:\n got: %q\nwant: %q", got, want)
	}
	if Rev(src) == Rev(src+" ") || len(Rev(src)) != 16 {
		t.Errorf("Rev should be a 16-character digest that changes with the content")
	}
}

func TestReplaceText(t *testing.T) {
	const src = "<body>\n<h1>Plans &amp; pricing</h1>\n<p class=\"lead\">A very <b>good</b> plan.\r\nTry it &lt;now&gt;.</p>\n<ul><li>one</li><li>two</li></ul>\n<script>var s = \"one\";</script>tail\n</body>"
	at := func(tag string) int {
		i := strings.Index(src, tag)
		if i == -1 {
			t.Fatalf("no %s in the test source", tag)
		}
		return i
	}
	tests := []struct {
		name string
		edit TextEdit
		want string // the changed line of the result, or "" if the edit must be refused
		line int
	}{
		{"plain text", TextEdit{At: at("<p"), Before: "A ", Old: "very", After: " ", New: "really"}, `<p class="lead">A really <b>good</b> plan.`, 3},
		{"text after an end tag belongs to the preceding start tag", TextEdit{At: at("<b>"), Before: " ", Old: "plan", After: ".\nTry it <now>.", New: "deal"}, `<p class="lead">A very <b>good</b> deal.`, 3},
		{"whole node", TextEdit{At: at("<b>"), Old: "good", New: "great"}, `<p class="lead">A very <b>great</b> plan.`, 3},
		{"second line of a node, after a CRLF", TextEdit{At: at("<b>"), Before: " plan.\nTry ", Old: "it", After: " <now>.", New: "this"}, "Try this &lt;now&gt;.</p>", 4},
		{"a character reference is replaced as a whole", TextEdit{At: at("<h1"), Before: "Plans ", Old: "&", After: " pricing", New: "and"}, "<h1>Plans and pricing</h1>", 2},
		{"selection ending at a reference", TextEdit{At: at("<b>"), Before: " plan.\nTry it ", Old: "<now>", After: ".", New: "today"}, "Try it today.</p>", 4},
		{"new text is escaped", TextEdit{At: at("<h1"), Before: "Plans & ", Old: "pricing", New: "<b>R&D</b>"}, "<h1>Plans &amp; &lt;b&gt;R&amp;D&lt;/b&gt;</h1>", 2},
		{"deletion", TextEdit{At: at("<p"), Before: "A", Old: " very", After: " ", New: ""}, `<p class="lead">A <b>good</b> plan.`, 3},
		{"same text in sibling elements is told apart by the tag", TextEdit{At: at("<li>two") /* second li */, Old: "two", New: "2"}, "<ul><li>one</li><li>2</li></ul>", 5},
		{"text after a script", TextEdit{At: at("<li>two"), Old: "tail", After: "\n", New: "end"}, `<script>var s = "one";</script>end`, 6},

		{"text that is not there", TextEdit{At: at("<p"), Old: "nope", New: "x"}, "", 0},
		{"right text, wrong tag", TextEdit{At: at("<h1"), Before: "A ", Old: "very", After: " ", New: "x"}, "", 0},
		{"offset that is not a start tag", TextEdit{At: at("<p") + 1, Before: "A ", Old: "very", After: " ", New: "x"}, "", 0},
		{"offset of an end tag", TextEdit{At: at("</h1>"), Before: "\n", Old: "A", New: "x"}, "", 0},
		{"script content is not text", TextEdit{At: at("<li>two"), Old: `var s = "one";`, New: "x"}, "", 0},
		{"empty selection", TextEdit{At: at("<p"), Before: "A very ", New: "x"}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, line, err := ReplaceText(src, tt.edit)
			if tt.want == "" {
				if err != ErrNotInSource || out != src {
					t.Errorf("want a refusal that leaves the source alone, got err=%v changed=%v", err, out != src)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReplaceText: %v", err)
			}
			if line != tt.line {
				t.Errorf("line = %d, want %d", line, tt.line)
			}
			if got := strings.TrimRight(strings.Split(out, "\n")[tt.line-1], "\r"); got != tt.want {
				t.Errorf("line %d:\n got: %q\nwant: %q", tt.line, got, tt.want)
			}
			// Nothing but the edited line may change.
			before, after := strings.Split(src, "\n"), strings.Split(out, "\n")
			if len(before) != len(after) {
				t.Fatalf("line count changed:\n%s", out)
			}
			for i := range before {
				if i != tt.line-1 && before[i] != after[i] {
					t.Errorf("line %d changed too: %q", i+1, after[i])
				}
			}
		})
	}
}

func TestReplaceTextRefusesAmbiguousAndSplitReferences(t *testing.T) {
	// The same text twice before the next start tag: no way to tell which was meant.
	twice := "<p>x<!-- c -->x</p>"
	if _, _, err := ReplaceText(twice, TextEdit{At: 0, Old: "x", New: "y"}); err != ErrNotInSource {
		t.Errorf("ambiguous text: err = %v", err)
	}
	// "&amp;" shows as one character; half of it cannot be selected.
	ref := "<p>a &amp;amp b</p>"
	if _, _, err := ReplaceText(ref, TextEdit{At: 0, Before: "a &a", Old: "mp", After: " b", New: "x"}); err != nil {
		t.Errorf("text after a reference should be editable: %v", err)
	}
	crlf := "<p>a\r\nb</p>"
	if out, _, err := ReplaceText(crlf, TextEdit{At: 0, Before: "a", Old: "\n", After: "b", New: " "}); err != nil || out != "<p>a b</p>" {
		t.Errorf("replacing a CRLF: %q, %v", out, err)
	}
	// Text after a tag that is never closed is left alone.
	open := "<p>ok</p>tail<div class=\"x"
	if out, _, err := ReplaceText(open, TextEdit{At: 0, Old: "ok", New: "fine"}); err != nil || out != "<p>fine</p>tail<div class=\"x" {
		t.Errorf("before an unterminated tag: %q, %v", out, err)
	}
}
