// Package store keeps review comments on disk: <root>/.inliner/comments.json
// is the source of truth and <root>/.inliner/feedback.md is regenerated from it
// on every write, so an AI can pick up the current feedback by reading one file.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DirName is the directory, under the project root, that holds the comments.
const DirName = ".inliner"

const (
	StatusOpen     = "open"
	StatusResolved = "resolved"

	KindText    = "text"
	KindElement = "element"
	KindPage    = "page"

	maxBody = 20_000
)

// Anchor says where in a page a comment points.
type Anchor struct {
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag,omitempty"`     // opening tag of the element, as HTML
	Line     int    `json:"line,omitempty"`    // source line of the nearest element from the file
	LineTag  string `json:"lineTag,omitempty"` // tag name found on that line, to detect stale lines
	Quote    string `json:"quote,omitempty"`   // text comments: the selected text, whitespace collapsed
	Prefix   string `json:"prefix,omitempty"`  // text just before the quote
	Suffix   string `json:"suffix,omitempty"`  // text just after the quote
	Text     string `json:"text,omitempty"`    // element comments: the element's text
}

// Resolution records who resolved a comment and what they said about it.
type Resolution struct {
	By   string `json:"by"`
	Note string `json:"note,omitempty"`
}

// Comment is one piece of feedback on one page.
type Comment struct {
	ID         int         `json:"id"`
	File       string      `json:"file"` // relative to the root, slash-separated
	Kind       string      `json:"kind"`
	Body       string      `json:"body"`
	Status     string      `json:"status"`
	Anchor     Anchor      `json:"anchor"`
	Anchored   bool        `json:"anchored"` // false once the browser can no longer find the target
	CreatedAt  string      `json:"createdAt"`
	UpdatedAt  string      `json:"updatedAt"`
	ResolvedAt string      `json:"resolvedAt,omitempty"`
	Resolution *Resolution `json:"resolution,omitempty"`
}

// NewComment is what a reviewer submits.
type NewComment struct {
	File   string `json:"file"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
	Anchor Anchor `json:"anchor"`
}

// Patch changes a comment's body and/or status; nil fields are left alone.
type Patch struct {
	Body   *string `json:"body"`
	Status *string `json:"status"`
}

// AnchorUpdate reports where the browser currently finds a comment.
type AnchorUpdate struct {
	ID       int    `json:"id"`
	Anchored *bool  `json:"anchored"` // nil means still anchored
	Line     int    `json:"line"`
	LineTag  string `json:"lineTag"`
}

// Error is a failure with the HTTP status the server should answer with:
// 400 for invalid input, 500 for a damaged store.
type Error struct {
	Msg    string
	Status int
}

func (e *Error) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Status: 400}
}

type data struct {
	Version  int       `json:"version"`
	NextID   int       `json:"nextId"`
	Comments []Comment `json:"comments"`
}

// Store reads and writes the comments of one project root.
type Store struct {
	Root         string
	Dir          string
	File         string
	FeedbackFile string

	mu sync.Mutex // serialises read-modify-write within this process
}

// New returns the store for the given project root.
func New(root string) *Store {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	dir := filepath.Join(root, DirName)
	return &Store{
		Root:         root,
		Dir:          dir,
		File:         filepath.Join(dir, "comments.json"),
		FeedbackFile: filepath.Join(dir, "feedback.md"),
	}
}

// NormalizeFile cleans a root-relative file reference. ok is false if it is
// empty or escapes the root.
func NormalizeFile(file string) (norm string, ok bool) {
	if file == "" || strings.ContainsRune(file, 0) {
		return "", false
	}
	norm = strings.TrimLeft(path.Clean(strings.ReplaceAll(file, `\`, "/")), "/")
	if norm == "" || norm == "." || norm == ".." || strings.HasPrefix(norm, "../") {
		return "", false
	}
	return norm, true
}

var shellSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// CLICommand is a shell command that runs this program from any directory on
// this machine: plain "inliner" when that is what PATH resolves to, otherwise
// the full path of the running binary.
func CLICommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "inliner"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if found, err := exec.LookPath("inliner"); err == nil {
		if real, err := filepath.EvalSymlinks(found); err == nil && real == exe {
			return "inliner"
		}
	}
	if shellSafe.MatchString(exe) {
		return exe
	}
	return strconv.Quote(exe)
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func truncate(s string, max int) string {
	if len(s) <= max { // bytes >= runes, so this is a cheap way out
		return s
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

func cleanAnchor(kind string, a Anchor) Anchor {
	if kind == KindPage {
		return Anchor{}
	}
	out := Anchor{
		Selector: truncate(a.Selector, 1000),
		Tag:      truncate(a.Tag, 300),
		LineTag:  truncate(a.LineTag, 40),
	}
	if a.Line > 0 {
		out.Line = a.Line
	}
	if kind == KindText {
		out.Quote = truncate(a.Quote, 5000)
		out.Prefix = truncate(a.Prefix, 64)
		out.Suffix = truncate(a.Suffix, 64)
	} else {
		out.Text = truncate(a.Text, 300)
	}
	return out
}

func cleanBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", invalid("Comment body is empty.")
	}
	return truncate(body, maxBody), nil
}

func setStatus(c *Comment, status, by, note string) {
	c.Status = status
	if status == StatusResolved {
		c.ResolvedAt = now()
		c.Resolution = &Resolution{By: by, Note: truncate(strings.TrimSpace(note), 2000)}
	} else {
		c.ResolvedAt = ""
		c.Resolution = nil
	}
}

func (s *Store) read() (*data, error) {
	raw, err := os.ReadFile(s.File)
	if errors.Is(err, fs.ErrNotExist) {
		return &data{Version: 1, NextID: 1, Comments: []Comment{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var d data
	if err := json.Unmarshal(raw, &d); err != nil {
		// Refuse to carry on: the next write would silently discard every comment.
		return nil, &Error{Msg: fmt.Sprintf("%s is not valid JSON (%v). Fix or delete it.", s.File, err), Status: 500}
	}
	d.Version = 1
	if d.Comments == nil {
		d.Comments = []Comment{}
	}
	for _, c := range d.Comments {
		if c.ID >= d.NextID {
			d.NextID = c.ID + 1
		}
	}
	if d.NextID < 1 {
		d.NextID = 1
	}
	return &d, nil
}

func (s *Store) write(d *data) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(s.Dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return err
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep quoted tags readable
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.File, os.Getpid())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.File); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.WriteFile(s.FeedbackFile, []byte(Render(d.Comments, RenderOptions{Root: s.Root, CLI: CLICommand()})), 0o644)
}

// update runs fn on the stored data under the lock and writes it back if fn
// reports a change.
func (s *Store) update(fn func(d *data) (dirty bool, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read()
	if err != nil {
		return err
	}
	dirty, err := fn(d)
	if err != nil || !dirty {
		return err
	}
	return s.write(d)
}

func find(d *data, id int) *Comment {
	for i := range d.Comments {
		if d.Comments[i].ID == id {
			return &d.Comments[i]
		}
	}
	return nil
}

// List returns all comments, or those on one file.
func (s *Store) List(file string) ([]Comment, error) {
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	if file == "" {
		return d.Comments, nil
	}
	out := []Comment{}
	for _, c := range d.Comments {
		if c.File == file {
			out = append(out, c)
		}
	}
	return out, nil
}

// Feedback renders the Markdown for the AI. file limits it to one page.
func (s *Store) Feedback(file string, includeResolved bool) (string, error) {
	comments, err := s.List(file)
	if err != nil {
		return "", err
	}
	return Render(comments, RenderOptions{Root: s.Root, CLI: CLICommand(), IncludeResolved: includeResolved}), nil
}

// Add stores a new open comment.
func (s *Store) Add(in NewComment) (Comment, error) {
	var created Comment
	err := s.update(func(d *data) (bool, error) {
		file, ok := NormalizeFile(in.File)
		if !ok {
			return false, invalid("Invalid file.")
		}
		if in.Kind != KindText && in.Kind != KindElement && in.Kind != KindPage {
			return false, invalid("Invalid comment kind.")
		}
		body, err := cleanBody(in.Body)
		if err != nil {
			return false, err
		}
		ts := now()
		created = Comment{
			ID:        d.NextID,
			File:      file,
			Kind:      in.Kind,
			Body:      body,
			Status:    StatusOpen,
			Anchor:    cleanAnchor(in.Kind, in.Anchor),
			Anchored:  true,
			CreatedAt: ts,
			UpdatedAt: ts,
		}
		d.NextID++
		d.Comments = append(d.Comments, created)
		return true, nil
	})
	return created, err
}

// Update edits a comment. It returns nil if there is no such comment.
func (s *Store) Update(id int, patch Patch) (*Comment, error) {
	var updated *Comment
	err := s.update(func(d *data) (bool, error) {
		c := find(d, id)
		if c == nil {
			return false, nil
		}
		if patch.Body != nil {
			body, err := cleanBody(*patch.Body)
			if err != nil {
				return false, err
			}
			c.Body = body
		}
		if patch.Status != nil {
			if *patch.Status != StatusOpen && *patch.Status != StatusResolved {
				return false, invalid("Invalid status.")
			}
			setStatus(c, *patch.Status, "reviewer", "")
		}
		c.UpdatedAt = now()
		copied := *c
		updated = &copied
		return true, nil
	})
	return updated, err
}

// Remove deletes comments and returns the ones that existed.
func (s *Store) Remove(ids []int) ([]Comment, error) {
	var removed []Comment
	err := s.update(func(d *data) (bool, error) {
		drop := map[int]bool{}
		for _, id := range ids {
			drop[id] = true
		}
		keep := d.Comments[:0:0]
		for _, c := range d.Comments {
			if drop[c.ID] {
				removed = append(removed, c)
			} else {
				keep = append(keep, c)
			}
		}
		d.Comments = keep
		return len(removed) > 0, nil
	})
	return removed, err
}

// SetStatus marks comments resolved or open. missing lists ids that do not exist.
func (s *Store) SetStatus(ids []int, status, by, note string) (changed []Comment, missing []int, err error) {
	err = s.update(func(d *data) (bool, error) {
		for _, id := range ids {
			c := find(d, id)
			if c == nil {
				missing = append(missing, id)
				continue
			}
			setStatus(c, status, by, note)
			c.UpdatedAt = now()
			changed = append(changed, *c)
		}
		return len(changed) > 0, nil
	})
	return changed, missing, err
}

// Clear deletes resolved comments, or everything with all. It returns how many went.
func (s *Store) Clear(all bool) (int, error) {
	removed := 0
	err := s.update(func(d *data) (bool, error) {
		keep := []Comment{}
		if !all {
			for _, c := range d.Comments {
				if c.Status != StatusResolved {
					keep = append(keep, c)
				}
			}
		}
		removed = len(d.Comments) - len(keep)
		d.Comments = keep
		return removed > 0, nil
	})
	return removed, err
}

// SyncAnchors records where the browser currently finds each comment (its line
// number, or that it is gone). This is bookkeeping, not an edit: UpdatedAt is
// left alone. It reports whether anything changed.
func (s *Store) SyncAnchors(file string, updates []AnchorUpdate) (bool, error) {
	changed := false
	err := s.update(func(d *data) (bool, error) {
		for _, u := range updates {
			c := find(d, u.ID)
			if c == nil || c.File != file || c.Kind == KindPage {
				continue
			}
			anchored := u.Anchored == nil || *u.Anchored
			if c.Anchored != anchored {
				c.Anchored = anchored
				changed = true
			}
			if anchored && u.Line > 0 {
				lineTag := truncate(u.LineTag, 40)
				if c.Anchor.Line != u.Line || c.Anchor.LineTag != lineTag {
					c.Anchor.Line = u.Line
					c.Anchor.LineTag = lineTag
					changed = true
				}
			}
		}
		return changed, nil
	})
	return changed, err
}
