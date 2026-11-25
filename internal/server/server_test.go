package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ntk221/inliner/internal/store"
)

const page = "<!doctype html>\n<html>\n<body>\n<h1>Title</h1>\n<p>Hello world</p>\n</body>\n</html>\n"

const comment = `{"file":"page.html","kind":"text","body":"Shorter","anchor":{"quote":"Hello","line":5,"lineTag":"p"}}`

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *Server {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	write(t, filepath.Join(root, "page.html"), page)
	write(t, filepath.Join(root, "style.css"), "body { color: red }")
	write(t, filepath.Join(root, ".env"), "SECRET=1")
	write(t, filepath.Join(root, "docs", "index.html"), "<p>docs</p>")
	write(t, filepath.Join(root, "docs", "deep", "a b.html"), "<p>deep</p>")
	write(t, filepath.Join(base, "outside.txt"), "outside")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink leading out of the root must not be followed.
	if err := os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Log("symlinks unavailable:", err)
	}
	srv, err := Start(Options{Root: root, Port: 0, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

type response struct {
	status int
	header http.Header
	body   string
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.body)
	}
}

// do sends a request without following redirects. Optional headers come in pairs.
func do(t *testing.T, method, url, body string, headers ...string) response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return response{res.StatusCode, res.Header, string(raw)}
}

func wantStatus(t *testing.T, r response, want int, what string) {
	t.Helper()
	if r.status != want {
		t.Errorf("%s: status %d, want %d (%s)", what, r.status, want, r.body)
	}
}

func wantMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("no match for %q in:\n%s", pattern, text)
	}
}

func TestServesPagesAndAssets(t *testing.T) {
	srv := setup(t)

	html := do(t, "GET", srv.URL+"/page.html", "")
	wantStatus(t, html, 200, "page")
	wantMatch(t, html.body, `<p data-inliner-line="5" data-inliner-at="\d+">Hello world</p>`)
	wantMatch(t, html.body, `<script src="/__inliner/client\.js" data-inliner-file="page\.html" data-inliner-rev="[0-9a-f]{16}" defer></script>\n</body>`)
	if got := html.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}

	css := do(t, "GET", srv.URL+"/style.css", "")
	if css.body != "body { color: red }" || css.header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("css = %q (%s)", css.body, css.header.Get("Content-Type"))
	}
	if part := do(t, "GET", srv.URL+"/style.css", "", "Range", "bytes=0-3"); part.status != 206 || part.body != "body" {
		t.Errorf("range request = %d %q", part.status, part.body)
	}

	client := do(t, "GET", srv.URL+"/__inliner/client.js", "")
	wantMatch(t, client.header.Get("Content-Type"), `javascript`)
	wantMatch(t, client.body, `inliner overlay`)

	wantStatus(t, do(t, "POST", srv.URL+"/page.html", ""), 405, "POST to a page")
}

func TestDirectories(t *testing.T) {
	srv := setup(t)
	wantStatus(t, do(t, "POST", srv.URL+"/__inliner/api/comments", comment), 201, "create")
	deep := strings.Replace(comment, "page.html", "docs/deep/a b.html", 1)
	wantStatus(t, do(t, "POST", srv.URL+"/__inliner/api/comments", deep), 201, "create deep")

	wantMatch(t, do(t, "GET", srv.URL+"/docs/", "").body, `data-inliner-file="docs/index\.html"`)

	listing := do(t, "GET", srv.URL+"/", "").body
	wantMatch(t, listing, `<a href="/page\.html">page\.html</a><span class="count">1</span>`)
	wantMatch(t, listing, `<a href="/docs/">docs/</a><span class="count">1</span>`)
	wantMatch(t, listing, `<a href="/empty/">empty/</a></li>`)
	for _, hidden := range []string{"style.css", ".env", ".inliner", "escape.txt"} {
		if strings.Contains(listing, hidden) {
			t.Errorf("listing shows %s", hidden)
		}
	}
	if strings.Index(listing, "docs/") > strings.Index(listing, "page.html") {
		t.Errorf("folders should come first")
	}

	sub := do(t, "GET", srv.URL+"/docs/deep/", "").body
	wantMatch(t, sub, `<a href="/docs/">\.\./</a>`)
	wantMatch(t, sub, `<a href="/docs/deep/a%20b\.html">a b\.html</a><span class="count">1</span>`)
	wantMatch(t, do(t, "GET", srv.URL+"/docs/deep/a%20b.html", "").body, `data-inliner-file="docs/deep/a b\.html"`)

	empty := do(t, "GET", srv.URL+"/empty/", "").body
	wantMatch(t, empty, `<a href="/">\.\./</a>`)
	wantMatch(t, empty, `No HTML files here`)

	redirect := do(t, "GET", srv.URL+"/docs?x=1", "")
	if redirect.status != 302 || redirect.header.Get("Location") != "/docs/?x=1" {
		t.Errorf("redirect = %d %q", redirect.status, redirect.header.Get("Location"))
	}
	// "//docs" must not turn into a redirect to another host.
	if loc := do(t, "GET", srv.URL+"//docs", "").header.Get("Location"); loc != "/docs/" {
		t.Errorf("redirect for //docs = %q", loc)
	}
}

func TestNeverServesDotfilesOrOutsideRoot(t *testing.T) {
	srv := setup(t)
	wantStatus(t, do(t, "POST", srv.URL+"/__inliner/api/comments", comment), 201, "create")
	for _, path := range []string{
		"/.env", "/.inliner/comments.json", "/nope.html", "/escape.txt",
		"/%2e%2e/outside.txt", "/docs/%2e%2e/%2e%2e/outside.txt", "/docs/..%5c..%5coutside.txt",
	} {
		wantStatus(t, do(t, "GET", srv.URL+path, ""), 404, path)
	}

	// Raw request: an HTTP client would normalise ".." away before sending.
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.URL.Opaque = "/../outside.txt"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("/../outside.txt: status %d, want 404", res.StatusCode)
	}
}

func TestRefusesOtherHostsAndCrossOriginWrites(t *testing.T) {
	srv := setup(t)
	wantStatus(t, do(t, "GET", srv.URL+"/page.html", "", "Host", "evil.example"), 403, "foreign host")
	wantStatus(t, do(t, "GET", srv.URL+"/page.html", "", "Host", fmt.Sprintf("127.0.0.1:%d", srv.Port)), 200, "127.0.0.1")

	api := srv.URL + "/__inliner/api/comments"
	wantStatus(t, do(t, "POST", api, comment, "Origin", "https://evil.example"), 403, "cross-origin POST")
	wantStatus(t, do(t, "DELETE", api+"/1", "", "Origin", "https://evil.example"), 403, "cross-origin DELETE")
	wantStatus(t, do(t, "POST", api, comment, "Origin", srv.URL), 201, "same-origin POST")
}

func TestCommentAPI(t *testing.T) {
	srv := setup(t)
	api := srv.URL + "/__inliner/api"

	var created store.Comment
	res := do(t, "POST", api+"/comments", comment)
	wantStatus(t, res, 201, "create")
	res.json(t, &created)
	if created.ID != 1 || created.Status != "open" || !created.Anchored || created.Anchor.Line != 5 {
		t.Errorf("created = %+v", created)
	}
	wantStatus(t, do(t, "POST", api+"/comments", strings.Replace(comment, "Shorter", " ", 1)), 400, "empty body")
	wantStatus(t, do(t, "POST", api+"/comments", strings.Replace(comment, "page.html", "../x.html", 1)), 400, "escaping file")
	wantStatus(t, do(t, "POST", api+"/comments", "{"), 400, "broken JSON")
	wantStatus(t, do(t, "POST", api+"/comments", `{"file":"page.html","kind":"page","body":"`+strings.Repeat("x", maxBody)+`"}`), 413, "oversized body")
	wantStatus(t, do(t, "GET", api+"/comments", ""), 400, "list without file")

	var listed struct{ Comments []store.Comment }
	do(t, "GET", api+"/comments?file=page.html", "").json(t, &listed)
	if len(listed.Comments) != 1 || listed.Comments[0].Body != "Shorter" {
		t.Errorf("listed = %+v", listed)
	}
	if other := do(t, "GET", api+"/comments?file=docs/index.html", "").body; !strings.Contains(other, `"comments":[]`) {
		t.Errorf("a page without comments should list an empty array: %s", other)
	}

	var edited store.Comment
	do(t, "PATCH", api+"/comments/1", `{"body":"Much shorter"}`).json(t, &edited)
	if edited.Body != "Much shorter" || edited.Status != "open" {
		t.Errorf("edited = %+v", edited)
	}

	feedback := do(t, "GET", api+"/feedback?file=page.html", "")
	wantMatch(t, feedback.header.Get("Content-Type"), `text/markdown`)
	wantMatch(t, feedback.body, "### #1 · `page\\.html:5` · text")
	wantMatch(t, feedback.body, `> Much shorter`)
	if onDisk, _ := os.ReadFile(srv.Store.FeedbackFile); string(onDisk) != do(t, "GET", api+"/feedback", "").body {
		t.Errorf("feedback.md differs from /feedback")
	}

	if synced := do(t, "POST", api+"/anchors", `{"file":"page.html","updates":[{"id":1,"anchored":false}]}`); synced.body != "{\"changed\":true}\n" {
		t.Errorf("anchors = %q", synced.body)
	}
	wantMatch(t, do(t, "GET", api+"/feedback", "").body, `was not found in the page as last rendered`)

	var resolved store.Comment
	do(t, "PATCH", api+"/comments/1", `{"status":"resolved"}`).json(t, &resolved)
	if resolved.Status != "resolved" || resolved.Resolution == nil || resolved.Resolution.By != "reviewer" {
		t.Errorf("resolved = %+v", resolved)
	}
	wantStatus(t, do(t, "PATCH", api+"/comments/1", `{"status":"bogus"}`), 400, "bogus status")
	wantStatus(t, do(t, "PATCH", api+"/comments/9", `{"body":"x"}`), 404, "edit missing")

	wantStatus(t, do(t, "DELETE", api+"/comments/1", ""), 200, "delete")
	wantStatus(t, do(t, "DELETE", api+"/comments/1", ""), 404, "delete again")
	wantStatus(t, do(t, "GET", api+"/nope", ""), 404, "unknown route")
}

func TestAnnouncesChanges(t *testing.T) {
	srv := setup(t)
	do(t, "GET", srv.URL+"/page.html", "") // a file is watched once a page has loaded it
	do(t, "GET", srv.URL+"/style.css", "")

	res, err := http.Get(srv.URL + "/__inliner/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	events := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(res.Body)
		name := ""
		for scanner.Scan() {
			line := scanner.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				events <- name + " " + v
			}
		}
		close(events)
	}()
	next := func(want string) {
		t.Helper()
		select {
		case got, ok := <-events:
			if !ok || got != want {
				t.Fatalf("event = %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	quiet := func(what string) {
		t.Helper()
		select {
		case got := <-events:
			t.Fatalf("%s: unexpected event %q", what, got)
		case <-time.After(150 * time.Millisecond):
		}
	}

	quiet("nothing changed")
	write(t, filepath.Join(srv.Root, "page.html"), strings.Replace(page, "Hello", "Goodbye, cruel", 1))
	next(`reload {"path":"page.html"}`)
	write(t, filepath.Join(srv.Root, "style.css"), "body { color: blue; }")
	next(`reload {"path":"style.css"}`)

	api := srv.URL + "/__inliner/api"
	wantStatus(t, do(t, "POST", api+"/comments", comment), 201, "create")
	next(`comments {}`)

	// Line bookkeeping from one tab is not announced to the others…
	do(t, "POST", api+"/anchors", `{"file":"page.html","updates":[{"id":1,"line":5,"lineTag":"h1"}]}`)
	quiet("anchor sync")
	// …but a change made by another process is (e.g. "inliner resolve 1").
	if _, _, err := store.New(srv.Root).SetStatus([]int{1}, store.StatusResolved, "cli", ""); err != nil {
		t.Fatal(err)
	}
	next(`comments {}`)
}

func TestPortFallback(t *testing.T) {
	first := setup(t)
	second, err := Start(Options{Root: first.Root, Port: first.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Port == first.Port {
		t.Errorf("second server should have moved to another port")
	}
	if _, err := Start(Options{Root: first.Root, Port: first.Port, StrictPort: true}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("strict port: err = %v", err)
	}
}

func TestCloseEndsEventStreams(t *testing.T) {
	srv := setup(t)
	res, err := http.Get(srv.URL + "/__inliner/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, res.Body)
		close(done)
	}()
	srv.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("event stream still open after Close")
	}
}

func TestEditWritesTextBackToTheFile(t *testing.T) {
	srv := setup(t)
	api := srv.URL + "/__inliner/api"
	file := filepath.Join(srv.Root, "page.html")
	read := func() string {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// What the browser knows: the revision it was served and where <p> starts.
	served := func() (rev, at string) {
		body := do(t, "GET", srv.URL+"/page.html", "").body
		rev = regexp.MustCompile(`data-inliner-rev="([0-9a-f]+)"`).FindStringSubmatch(body)[1]
		at = regexp.MustCompile(`<p data-inliner-line="5" data-inliner-at="(\d+)"`).FindStringSubmatch(body)[1]
		return rev, at
	}
	edit := func(rev, at, rest string) response {
		return do(t, "POST", api+"/edits", fmt.Sprintf(`{"file":"page.html","rev":%q,"at":%s,%s}`, rev, at, rest))
	}

	rev, at := served()
	res := edit(rev, at, `"before":"Hello ","old":"world","after":"","new":"<everyone> & you"`)
	wantStatus(t, res, 200, "edit")
	if res.body != "{\"line\":5}\n" {
		t.Errorf("edit response = %q", res.body)
	}
	if want := strings.Replace(page, "Hello world", "Hello &lt;everyone&gt; &amp; you", 1); read() != want {
		t.Errorf("file after edit:\n%s", read())
	}
	wantMatch(t, do(t, "GET", api+"/feedback", "").body, "## Edited directly by the reviewer[\\s\\S]*`page\\.html` \\(line 5 at the time\\): \"world\" → \"<everyone> & you\"")

	// The page the browser still shows is now out of date: its offsets must not be trusted.
	after := read()
	wantStatus(t, edit(rev, at, `"before":"","old":"Hello","after":" <everyone> & you","new":"Bye"`), 409, "stale revision")

	rev, at = served()
	wantStatus(t, edit(rev, at, `"before":"","old":"Nothing like this","after":"","new":"x"`), 422, "text not in the source")
	wantStatus(t, edit(rev, "1", `"before":"","old":"Hello","after":" <everyone> & you","new":"x"`), 422, "offset that is not a tag")
	if read() != after {
		t.Errorf("a refused edit changed the file")
	}
	wantStatus(t, edit(rev, at, `"before":"","old":"Hello","after":" <everyone> & you","new":"Bye"`), 200, "edit with the fresh revision")
	if !strings.Contains(read(), "<p>Bye &lt;everyone&gt; &amp; you</p>") {
		t.Errorf("file after second edit:\n%s", read())
	}

	for body, want := range map[string]int{
		`{"file":"style.css","rev":"x","at":0,"old":"body","new":"x"}`:          400,
		`{"file":"nope.html","rev":"x","at":0,"old":"a","new":"x"}`:             404,
		`{"file":".inliner/x.html","rev":"x","at":0,"old":"a","new":"x"}`:       404,
		`{"file":"../outside.html","rev":"x","at":0,"old":"a","new":"x"}`:       400,
		`{"file":"docs/deep/a b.html","rev":"x","at":0,"old":"deep","new":"x"}`: 409,
	} {
		wantStatus(t, do(t, "POST", api+"/edits", body), want, body)
	}
	wantStatus(t, do(t, "POST", api+"/edits", `{"file":"page.html"}`, "Origin", "https://evil.example"), 403, "cross-origin edit")
}
