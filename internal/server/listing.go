package server

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ntk221/inliner/internal/store"
)

func encodePath(rel string) string {
	segments := strings.Split(rel, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

const listingStyle = `
  :root { color-scheme: light dark; }
  body { font: 15px/1.6 system-ui, -apple-system, "Segoe UI", "Hiragino Sans", "Noto Sans JP", sans-serif; max-width: 720px; margin: 48px auto; padding: 0 20px; }
  h1 { font-size: 15px; font-weight: 600; margin: 0 0 4px; }
  h1 span { font-weight: 400; }
  p { color: GrayText; margin: 0 0 20px; font-size: 13px; }
  ul { list-style: none; padding: 0; margin: 0; border-top: 1px solid color-mix(in srgb, CanvasText 12%, transparent); }
  li { display: flex; align-items: center; gap: 10px; padding: 8px 4px; border-bottom: 1px solid color-mix(in srgb, CanvasText 12%, transparent); }
  a { color: LinkText; text-decoration: none; overflow-wrap: anywhere; }
  a:hover { text-decoration: underline; }
  .count { margin-left: auto; background: #e8930c; color: #fff; border-radius: 999px; padding: 0 8px; font-size: 12px; font-weight: 600; flex: none; }
  .empty { color: GrayText; padding: 16px 4px; }
`

// sendListing shows the folders and HTML files of a directory, with the number
// of open comments on each.
func (s *Server) sendListing(w http.ResponseWriter, abs, rel string) error {
	comments, err := s.Store.List("")
	if err != nil {
		return err
	}
	open := map[string]int{}
	for _, c := range comments {
		if c.Status == store.StatusOpen {
			open[c.File]++
		}
	}

	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	type entry struct {
		name string
		dir  bool
	}
	var entries []entry
	for _, e := range dirEntries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		st, err := os.Stat(filepath.Join(abs, name)) // follows symlinks
		if err != nil {
			continue
		}
		if st.IsDir() || isHTML(name) {
			entries = append(entries, entry{name, st.IsDir()})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].dir != entries[j].dir {
			return entries[i].dir
		}
		return entries[i].name < entries[j].name
	})

	var rows strings.Builder
	if rel != "" {
		parent := "/"
		if dir := path.Dir(rel); dir != "." {
			parent = "/" + encodePath(dir) + "/"
		}
		fmt.Fprintf(&rows, "<li><a href=\"%s\">../</a></li>\n", parent)
	}
	for _, e := range entries {
		child := strings.TrimPrefix(rel+"/"+e.name, "/")
		count, slash := open[child], ""
		if e.dir {
			slash = "/"
			count = 0
			for file, n := range open {
				if strings.HasPrefix(file, child+"/") {
					count += n
				}
			}
		}
		badge := ""
		if count > 0 {
			badge = fmt.Sprintf(`<span class="count">%d</span>`, count)
		}
		fmt.Fprintf(&rows, "<li><a href=\"/%s%s\">%s%s</a>%s</li>\n", encodePath(child), slash, html.EscapeString(e.name), slash, badge)
	}

	list, empty := "", ""
	if rows.Len() > 0 {
		list = "<ul>\n" + rows.String() + "</ul>\n"
	}
	if len(entries) == 0 {
		empty = `<div class="empty">No HTML files here.</div>` + "\n"
	}
	title := html.EscapeString("/" + rel)
	w.Header().Set("Content-Type", mimeTypes[".html"])
	_, err = io.WriteString(w, `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>inliner — `+title+`</title>
<style>`+listingStyle+`</style>
<h1>inliner <span>`+title+`</span></h1>
<p>`+html.EscapeString(s.Root)+`</p>
`+list+empty+`</html>
`)
	return err
}
