// Package server is the local review server: it serves a directory as-is, adds
// the comment overlay to HTML pages, stores comments, and tells open pages when
// files change.
package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ntk221/inliner/internal/annotate"
	"github.com/ntk221/inliner/internal/store"
)

//go:embed client.js
var clientJS []byte

const (
	apiPath    = "/__inliner/api"
	eventsPath = "/__inliner/events"
	maxBody    = 1 << 20

	// DefaultPort is used when the caller has no preference.
	DefaultPort = 4600
)

var mimeTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
	".md":    "text/plain; charset=utf-8",
	".csv":   "text/csv; charset=utf-8",
	".xml":   "application/xml; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".mp3":   "audio/mpeg",
	".wav":   "audio/wav",
	".pdf":   "application/pdf",
	".wasm":  "application/wasm",
}

func isHTML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".htm"
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

var errNotFound = &httpError{http.StatusNotFound, "Not found."}

// Options configures Start.
type Options struct {
	Root         string        // directory to serve; comments are stored under it
	Port         int           // 0 picks a free port
	StrictPort   bool          // fail instead of trying the next ports when Port is taken
	PollInterval time.Duration // how often to check files for changes (default 400ms)
}

// Server is a running review server.
type Server struct {
	URL   string
	Port  int
	Root  string
	Store *store.Store

	http *http.Server
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	clients  map[chan string]struct{}
	watched  map[string]*watchEntry // absolute path -> state
	storeSig string
}

type watchEntry struct{ rel, sig string }

// Start begins serving and returns once the port is open.
func Start(opts Options) (*Server, error) {
	root, err := filepath.Abs(opts.Root)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return nil, err
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 400 * time.Millisecond
	}

	var ln net.Listener
	for attempt := 0; ; attempt++ {
		port := opts.Port
		if port != 0 {
			port += attempt
		}
		ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			break
		}
		if !addrInUse(err) {
			return nil, err
		}
		if opts.StrictPort || opts.Port == 0 || attempt >= 20 {
			return nil, fmt.Errorf("port %d is already in use", port)
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port

	s := &Server{
		URL:     fmt.Sprintf("http://localhost:%d", port),
		Port:    port,
		Root:    root,
		Store:   store.New(root),
		done:    make(chan struct{}),
		clients: map[chan string]struct{}{},
		watched: map[string]*watchEntry{},
	}
	s.storeSig = signature(s.Store.File)
	s.http = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	go s.http.Serve(ln)
	go s.poll(opts.PollInterval)
	return s, nil
}

func addrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	// Windows reports WSAEADDRINUSE, which does not compare equal to EADDRINUSE.
	return strings.Contains(err.Error(), "Only one usage of each socket address")
}

// Close stops the server and drops every open connection.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.http.Close()
	})
	return err
}

// --- change detection --------------------------------------------------------
// Poll the files that pages have actually loaded. Unlike file-system
// notifications this behaves the same on every platform and survives editors
// that save by rename.

func signature(abs string) string {
	st, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
}

func (s *Server) watch(abs, rel string) {
	sig := signature(abs)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.watched) < 5000 {
		s.watched[abs] = &watchEntry{rel: rel, sig: sig}
	}
}

func event(name string, data any) string {
	payload, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, payload)
}

func (s *Server) poll(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ticks := 1; ; ticks++ {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		var messages []string
		if len(s.clients) > 0 {
			for abs, entry := range s.watched {
				sig := signature(abs)
				if sig == "" || sig == entry.sig {
					continue // a deleted file is not news until it comes back
				}
				entry.sig = sig
				messages = append(messages, event("reload", map[string]string{"path": entry.rel}))
			}
			if sig := signature(s.Store.File); sig != s.storeSig {
				s.storeSig = sig
				messages = append(messages, event("comments", struct{}{}))
			}
			if ticks%60 == 0 {
				messages = append(messages, ": keepalive\n\n")
			}
		}
		for ch := range s.clients {
			for _, msg := range messages {
				select {
				case ch <- msg:
				default: // a stalled page misses an event rather than blocking everyone
				}
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming is not supported by this connection")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, "retry: 1000\n\n")
	flusher.Flush()

	ch := make(chan string, 32)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	for {
		select {
		case msg := <-ch:
			if _, err := io.WriteString(w, msg); err != nil {
				return nil
			}
			flusher.Flush()
		case <-r.Context().Done():
			return nil
		case <-s.done:
			return nil
		}
	}
}

// --- request handling ----------------------------------------------------------

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	err := s.route(w, r)
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	msg := "Internal error."
	var he *httpError
	var se *store.Error
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &he):
		status, msg = he.status, he.msg
	case errors.As(err, &se):
		status, msg = se.Status, se.Msg
	case errors.As(err, &tooBig):
		status, msg = http.StatusRequestEntityTooLarge, "Request body too large."
	case errors.Is(err, fs.ErrNotExist):
		status, msg = http.StatusNotFound, "Not found."
	}
	if status == http.StatusInternalServerError {
		fmt.Fprintf(os.Stderr, "inliner: %s %s: %v\n", r.Method, r.URL.Path, err)
	}
	sendJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) error {
	// Refuse anything not addressed to localhost: this stops DNS-rebinding pages
	// from reading local files through this server.
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
	default:
		return &httpError{http.StatusForbidden, "Forbidden host."}
	}

	p := r.URL.Path
	switch {
	case p == annotate.ClientPath:
		w.Header().Set("Content-Type", mimeTypes[".js"])
		w.Write(clientJS)
		return nil
	case p == eventsPath:
		return s.handleEvents(w, r)
	case p == apiPath || strings.HasPrefix(p, apiPath+"/"):
		return s.handleAPI(w, r, strings.TrimPrefix(p, apiPath))
	}
	return s.handleStatic(w, r)
}

func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", mimeTypes[".json"])
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil, errors.Is(err, io.EOF): // an empty body is an empty object
		return nil
	case errors.As(err, &tooBig):
		return err
	}
	return &httpError{http.StatusBadRequest, "Request body is not valid JSON."}
}

func requireFile(value string) (string, error) {
	file, ok := store.NormalizeFile(value)
	if !ok {
		return "", &httpError{http.StatusBadRequest, `Missing or invalid "file".`}
	}
	return file, nil
}

var commentRoute = regexp.MustCompile(`^/comments/(\d{1,9})$`)

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, route string) error {
	if r.Method != http.MethodGet {
		// Only pages served by this server may change comments.
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			return &httpError{http.StatusForbidden, "Cross-origin request refused."}
		}
	}

	switch {
	case route == "/comments" && r.Method == http.MethodGet:
		file, err := requireFile(r.URL.Query().Get("file"))
		if err != nil {
			return err
		}
		comments, err := s.Store.List(file)
		if err != nil {
			return err
		}
		sendJSON(w, http.StatusOK, map[string]any{"file": file, "comments": comments})
		return nil

	case route == "/comments" && r.Method == http.MethodPost:
		var in store.NewComment
		if err := readJSON(w, r, &in); err != nil {
			return err
		}
		file, err := requireFile(in.File)
		if err != nil {
			return err
		}
		in.File = file
		created, err := s.Store.Add(in)
		if err != nil {
			return err
		}
		sendJSON(w, http.StatusCreated, created)
		return nil

	case route == "/feedback" && r.Method == http.MethodGet:
		file := r.URL.Query().Get("file")
		if file != "" {
			var err error
			if file, err = requireFile(file); err != nil {
				return err
			}
		}
		text, err := s.Store.Feedback(file, false)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, text)
		return nil

	case route == "/anchors" && r.Method == http.MethodPost:
		var in struct {
			File    string               `json:"file"`
			Updates []store.AnchorUpdate `json:"updates"`
		}
		if err := readJSON(w, r, &in); err != nil {
			return err
		}
		file, err := requireFile(in.File)
		if err != nil {
			return err
		}
		// Line bookkeeping is not news for other tabs: record the new state of
		// the store as already seen, so that it is not announced.
		s.mu.Lock()
		changed, err := s.Store.SyncAnchors(file, in.Updates)
		if changed {
			s.storeSig = signature(s.Store.File)
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		sendJSON(w, http.StatusOK, map[string]bool{"changed": changed})
		return nil
	}

	if m := commentRoute.FindStringSubmatch(route); m != nil {
		id, _ := strconv.Atoi(m[1])
		missing := &httpError{http.StatusNotFound, fmt.Sprintf("No comment #%d.", id)}
		switch r.Method {
		case http.MethodPatch:
			var patch store.Patch
			if err := readJSON(w, r, &patch); err != nil {
				return err
			}
			updated, err := s.Store.Update(id, patch)
			if err != nil {
				return err
			}
			if updated == nil {
				return missing
			}
			sendJSON(w, http.StatusOK, updated)
			return nil
		case http.MethodDelete:
			removed, err := s.Store.Remove([]int{id})
			if err != nil {
				return err
			}
			if len(removed) == 0 {
				return missing
			}
			sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return nil
		}
	}
	return &httpError{http.StatusNotFound, "Unknown API route."}
}

// --- static files --------------------------------------------------------------

// resolve maps a URL path to a file inside the root. rel is the cleaned,
// slash-separated path relative to the root.
func (s *Server) resolve(urlPath string) (abs, rel string, err error) {
	var segments []string
	for _, seg := range strings.Split(urlPath, "/") {
		if seg == "" {
			continue
		}
		// Dot-prefixed entries (.git, .env, .inliner, and of course "..") are never served.
		if strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "\\\x00") {
			return "", "", errNotFound
		}
		segments = append(segments, seg)
	}
	abs = filepath.Join(append([]string{s.Root}, segments...)...)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", errNotFound
	}
	if real != s.Root && !strings.HasPrefix(real, s.Root+string(filepath.Separator)) {
		return "", "", errNotFound // a symlink pointing out of the root
	}
	return abs, strings.Join(segments, "/"), nil
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return &httpError{http.StatusMethodNotAllowed, "Method not allowed."}
	}
	abs, rel, err := s.resolve(r.URL.Path)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}

	if st.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			// Built from the cleaned path, so "//host" can never become the target.
			location := "/" + encodePath(rel) + "/"
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
			return nil
		}
		index := filepath.Join(abs, "index.html")
		if ist, err := os.Stat(index); err == nil && !ist.IsDir() {
			return s.sendPage(w, index, strings.TrimPrefix(rel+"/index.html", "/"))
		}
		return s.sendListing(w, abs, rel)
	}
	if isHTML(abs) {
		return s.sendPage(w, abs, rel)
	}

	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	s.watch(abs, rel)
	contentType := mimeTypes[strings.ToLower(filepath.Ext(abs))]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, "", time.Time{}, f) // handles Range, for video and audio
	return nil
}

func (s *Server) sendPage(w http.ResponseWriter, abs, rel string) error {
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	s.watch(abs, rel)
	w.Header().Set("Content-Type", mimeTypes[".html"])
	io.WriteString(w, annotate.Prepare(string(src), rel))
	return nil
}
