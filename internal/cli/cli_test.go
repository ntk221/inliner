package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ntk221/inliner/internal/store"
)

func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"out/page.html": "<body>\n<p>Hello world</p>\n</body>\n",
		"other.html":    "<body>\n<h1>Other</h1>\n</body>\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := store.New(root)
	for _, c := range []store.NewComment{
		{File: "out/page.html", Kind: store.KindText, Body: "Shorter", Anchor: store.Anchor{Quote: "Hello", Line: 2, LineTag: "p"}},
		{File: "other.html", Kind: store.KindPage, Body: "Needs a footer"},
	} {
		if _, err := st.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type result struct {
	code     int
	out, err string
}

func run(t *testing.T, root string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), append([]string{"-C", root}, args...), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func wantMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("no match for %q in:\n%s", pattern, text)
	}
}

func TestShow(t *testing.T) {
	root := project(t)

	all := run(t, root, "show")
	if all.code != 0 {
		t.Fatalf("show: %+v", all)
	}
	wantMatch(t, all.out, "### #1 · `out/page\\.html:2` · text")
	wantMatch(t, all.out, "### #2 · `other\\.html` · page")

	one := run(t, root, "show", filepath.Join(root, "out", "page.html"))
	wantMatch(t, one.out, `Open comments: 1`)
	if strings.Contains(one.out, "other.html") {
		t.Errorf("show <file> leaked another file:\n%s", one.out)
	}

	var listed []store.Comment
	if err := json.Unmarshal([]byte(run(t, root, "show", "--json").out), &listed); err != nil || len(listed) != 2 {
		t.Errorf("show --json: %v, %d comments", err, len(listed))
	}
	if r := run(t, root, "show", filepath.Join(filepath.Dir(root), "elsewhere.html")); r.code != 2 {
		t.Errorf("file outside the root: %+v", r)
	}
	if r := run(t, root, "show", "a.html", "b.html"); r.code != 2 {
		t.Errorf("two files: %+v", r)
	}
}

func TestResolveReopenRmClear(t *testing.T) {
	root := project(t)

	// Options may come after the arguments, as people type them.
	if r := run(t, root, "resolve", "#1", "-m", "Trimmed the intro"); r != (result{0, "#1 resolved\n", ""}) {
		t.Errorf("resolve: %+v", r)
	}
	list, _ := store.New(root).List("")
	if r := list[0].Resolution; r == nil || *r != (store.Resolution{By: "cli", Note: "Trimmed the intro"}) {
		t.Errorf("resolution = %+v", r)
	}
	if out := run(t, root, "show").out; strings.Contains(out, "### #1 ") {
		t.Errorf("resolved comment still shown:\n%s", out)
	}
	wantMatch(t, run(t, root, "show", "--all").out, `Status: resolved — Trimmed the intro`)

	missing := run(t, root, "resolve", "2", "9", "--message=done")
	if missing.code != 1 || missing.out != "#2 resolved\n" || !strings.Contains(missing.err, "no comment #9") {
		t.Errorf("resolve with a missing id: %+v", missing)
	}

	if r := run(t, root, "reopen", "2"); r.out != "#2 reopened\n" {
		t.Errorf("reopen: %+v", r)
	}
	if r := run(t, root, "clear"); r.out != "1 comment deleted\n" {
		t.Errorf("clear: %+v", r)
	}
	if r := run(t, root, "rm", "2"); r.out != "#2 deleted\n" {
		t.Errorf("rm: %+v", r)
	}
	if r := run(t, root, "rm", "2"); r.code != 1 {
		t.Errorf("rm again: %+v", r)
	}
	if r := run(t, root, "show"); r.out != "# Inline review comments\n\nNo open comments.\n" {
		t.Errorf("show after clearing: %q", r.out)
	}
	if r := run(t, root, "clear", "--all"); r.out != "0 comments deleted\n" {
		t.Errorf("clear --all: %+v", r)
	}
}

func TestUsageErrors(t *testing.T) {
	root := project(t)
	for _, args := range [][]string{
		{"resolve"},
		{"resolve", "abc"},
		{"resolve", "0"},
		{"--bogus"},
		{"--port"},
		{"--all=yes", "show"},
		{filepath.Join(root, "no-such-file.html"), "--no-open"},
		{filepath.Join(root, "out", "page.html"), "--no-open", "-p", "abc"},
		{filepath.Join(root, "a.html"), filepath.Join(root, "b.html")},
	} {
		if r := run(t, root, args...); r.code != 2 || r.err == "" {
			t.Errorf("%v: %+v, want exit code 2 and a message", args, r)
		}
	}
	if r := run(t, root, "--help"); r.code != 0 || !strings.Contains(r.out, "Usage") {
		t.Errorf("--help: %+v", r)
	}
	if r := run(t, root, "help"); r.code != 0 || !strings.Contains(r.out, "Usage") {
		t.Errorf("help: %+v", r)
	}
	if r := run(t, root, "--version"); r.code != 0 || strings.TrimSpace(r.out) == "" {
		t.Errorf("--version: %+v", r)
	}
}

func TestCorruptStoreIsAnError(t *testing.T) {
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, ".inliner", "comments.json"), []byte("{ nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := run(t, root, "show"); r.code != 1 || !strings.Contains(r.err, "not valid JSON") {
		t.Errorf("show with a corrupt store: %+v", r)
	}
}

// syncBuffer lets the test read what the server goroutine prints.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServe(t *testing.T) {
	root := project(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	exited := make(chan int, 1)
	go func() {
		exited <- Run(ctx, []string{filepath.Join(root, "out", "page.html"), "-C", root, "--no-open", "-p", "0"}, &out, io.Discard)
	}()

	urlPattern := regexp.MustCompile(`http://localhost:\d+/out/page\.html`)
	var pageURL string
	for deadline := time.Now().Add(5 * time.Second); pageURL == ""; {
		if pageURL = urlPattern.FindString(out.String()); pageURL == "" {
			if time.Now().After(deadline) {
				t.Fatalf("no URL printed:\n%s", out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wantMatch(t, out.String(), `feedback  .*feedback\.md`)

	res, err := http.Get(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	wantMatch(t, string(body), `data-inliner-file="out/page\.html"`)

	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop when the context was cancelled")
	}
	if _, err := http.Get(pageURL); err == nil {
		t.Errorf("server still answering after shutdown")
	}
}
