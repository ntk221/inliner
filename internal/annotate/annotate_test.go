package annotate

import (
	"strings"
	"testing"
)

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
			got := Lines(tt.src)
			if got != tt.want {
				t.Errorf("Lines:\n got: %q\nwant: %q", got, tt.want)
			}
			if strings.Count(got, "\n") != strings.Count(tt.src, "\n") {
				t.Errorf("line count changed")
			}
		})
	}
}

func TestInjectClient(t *testing.T) {
	const tag = `<script src="/__inliner/client.js" data-inliner-file="a/b.html" defer></script>`
	if got, want := InjectClient("<body><p>x</p></BODY></html>", "a/b.html"), "<body><p>x</p>"+tag+"\n</BODY></html>"; got != want {
		t.Errorf("before </body>:\n got: %q\nwant: %q", got, want)
	}
	if got, want := InjectClient("<p>x</p>", "a/b.html"), "<p>x</p>\n"+tag+"\n"; got != want {
		t.Errorf("no body:\n got: %q\nwant: %q", got, want)
	}
	if got := InjectClient("", `we"ird<.html`); !strings.Contains(got, `data-inliner-file="we&quot;ird&lt;.html"`) {
		t.Errorf("file name not escaped: %q", got)
	}
}

func TestPrepare(t *testing.T) {
	got := Prepare("<body>\n<p>x</p>\n</body>", "p.html")
	want := "<body data-inliner-line=\"1\">\n<p data-inliner-line=\"2\">x</p>\n<script src=\"/__inliner/client.js\" data-inliner-file=\"p.html\" defer></script>\n</body>"
	if got != want {
		t.Errorf("Prepare:\n got: %q\nwant: %q", got, want)
	}
}
