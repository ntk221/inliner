// Package cli implements the inliner command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/ntk221/inliner/internal/server"
	"github.com/ntk221/inliner/internal/store"
)

const usage = `inliner — comment inline on AI-generated HTML, hand the feedback back to the AI

Usage
  inliner [path]              Open an HTML file or directory for review (default: .)
  inliner show [file]         Print open comments as Markdown (what the AI should read)
  inliner resolve <id...>     Mark comments resolved        (-m "note" to say what changed)
  inliner reopen <id...>      Reopen resolved comments
  inliner rm <id...>          Delete comments
  inliner clear               Delete resolved comments and the record of direct edits
                              (--all: delete every comment too)

Options
  -p, --port <n>     Port to listen on (default 4600, next free port if taken)
      --no-open      Do not open the browser
  -C, --root <dir>   Project root: where .inliner/ lives and what file paths are relative to
                     (default: the current directory)
  -m, --message <s>  Note to attach when resolving
      --all          show: include resolved comments / clear: delete everything
      --json         show: print raw JSON
  -h, --help         Show this help
  -v, --version      Show version

Comments are stored in .inliner/comments.json; .inliner/feedback.md always holds the
current open comments, ready to be read by an AI.
`

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return &usageError{fmt.Sprintf(format, args...)} }

type options struct {
	port    string
	hasPort bool
	noOpen  bool
	root    string
	message string
	all     bool
	json    bool
	help    bool
	version bool
	args    []string // positional arguments
}

// parse accepts options before, between and after positional arguments.
func parse(argv []string) (*options, error) {
	o := &options{}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			o.args = append(o.args, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			o.args = append(o.args, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(argv) {
				return "", usagef("option %s needs a value", name)
			}
			i++
			return argv[i], nil
		}
		flag := func(target *bool) error {
			if hasInline {
				return usagef("option %s does not take a value", name)
			}
			*target = true
			return nil
		}

		var err error
		switch name {
		case "-p", "--port":
			o.port, err = value()
			o.hasPort = true
		case "-C", "--root":
			o.root, err = value()
		case "-m", "--message":
			o.message, err = value()
		case "--no-open":
			err = flag(&o.noOpen)
		case "--all":
			err = flag(&o.all)
		case "--json":
			err = flag(&o.json)
		case "-h", "--help":
			err = flag(&o.help)
		case "-v", "--version":
			err = flag(&o.version)
		default:
			err = usagef("unknown option %s", name)
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

var commands = map[string]bool{"serve": true, "show": true, "resolve": true, "reopen": true, "rm": true, "clear": true, "help": true}

// Run executes the command line and returns the process exit code. A running
// server stops when ctx is cancelled.
func Run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	o, err := parse(argv)
	if err != nil {
		fmt.Fprintf(stderr, "inliner: %v\n\n%s", err, usage)
		return 2
	}
	if o.version {
		fmt.Fprintln(stdout, version())
		return 0
	}
	command := "serve"
	if len(o.args) > 0 && commands[o.args[0]] {
		command, o.args = o.args[0], o.args[1:]
	}
	if o.help || command == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	code, err := dispatch(ctx, command, o, stdout, stderr)
	var ue *usageError
	switch {
	case err == nil:
		return code
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "inliner: %v\n", err)
		return 2
	}
	fmt.Fprintf(stderr, "inliner: %v\n", err)
	return 1
}

func dispatch(ctx context.Context, command string, o *options, stdout, stderr io.Writer) (int, error) {
	if command == "serve" {
		if len(o.args) > 1 {
			return 0, usagef("give a single file or directory")
		}
		target := "."
		if len(o.args) == 1 {
			target = o.args[0]
		}
		return 0, serve(ctx, target, o, stdout)
	}

	root := o.root
	if root == "" {
		root = "."
	}
	st := store.New(root)

	switch command {
	case "show":
		if len(o.args) > 1 {
			return 0, usagef("give at most one file")
		}
		return 0, show(st, o, stdout)

	case "resolve", "reopen":
		ids, err := parseIDs(o.args)
		if err != nil {
			return 0, err
		}
		status, done := store.StatusResolved, "resolved"
		if command == "reopen" {
			status, done = store.StatusOpen, "reopened"
		}
		changed, missing, err := st.SetStatus(ids, status, "cli", o.message)
		if err != nil {
			return 0, err
		}
		for _, c := range changed {
			fmt.Fprintf(stdout, "#%d %s\n", c.ID, done)
		}
		return reportMissing(stderr, missing), nil

	case "rm":
		ids, err := parseIDs(o.args)
		if err != nil {
			return 0, err
		}
		removed, err := st.Remove(ids)
		if err != nil {
			return 0, err
		}
		gone := map[int]bool{}
		for _, c := range removed {
			gone[c.ID] = true
			fmt.Fprintf(stdout, "#%d deleted\n", c.ID)
		}
		var missing []int
		for _, id := range ids {
			if !gone[id] {
				missing = append(missing, id)
			}
		}
		return reportMissing(stderr, missing), nil

	case "clear":
		n, err := st.Clear(o.all)
		if err != nil {
			return 0, err
		}
		plural := "s"
		if n == 1 {
			plural = ""
		}
		fmt.Fprintf(stdout, "%d comment%s deleted\n", n, plural)
	}
	return 0, nil
}

func reportMissing(stderr io.Writer, missing []int) int {
	for _, id := range missing {
		fmt.Fprintf(stderr, "inliner: no comment #%d\n", id)
	}
	if len(missing) > 0 {
		return 1
	}
	return 0
}

func parseIDs(args []string) ([]int, error) {
	if len(args) == 0 {
		return nil, usagef("give at least one comment id")
	}
	ids := make([]int, 0, len(args))
	for _, a := range args {
		id, err := strconv.Atoi(strings.TrimPrefix(a, "#"))
		if err != nil || id < 1 {
			return nil, usagef("not a comment id: %s", a)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func show(st *store.Store, o *options, stdout io.Writer) error {
	file := ""
	if len(o.args) == 1 {
		abs, err := filepath.Abs(o.args[0])
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(st.Root, abs)
		if err != nil {
			return usagef("%s is outside the root %s", o.args[0], st.Root)
		}
		var ok bool
		if file, ok = store.NormalizeFile(filepath.ToSlash(rel)); !ok {
			return usagef("%s is outside the root %s", o.args[0], st.Root)
		}
	}

	if !o.json {
		text, err := st.Feedback(file, o.all)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, text)
		return err
	}
	comments, err := st.List(file)
	if err != nil {
		return err
	}
	shown := []store.Comment{}
	for _, c := range comments {
		if o.all || c.Status == store.StatusOpen {
			shown = append(shown, c)
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(shown)
}

func inside(dir, target string) bool {
	return target == dir || strings.HasPrefix(target, dir+string(filepath.Separator))
}

func serve(ctx context.Context, target string, o *options, stdout io.Writer) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return usagef("no such file or directory: %s", target)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}

	// The root is where the AI works from, so that file:line in the feedback
	// matches the paths it sees. Fall back to the target's own directory when
	// the target lies somewhere else.
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		return err
	}
	root := cwd
	if o.root != "" {
		if root, err = filepath.Abs(o.root); err == nil {
			root, err = filepath.EvalSymlinks(root)
		}
		if err != nil {
			return usagef("no such directory: %s", o.root)
		}
		if !inside(root, real) {
			return usagef("%s is not inside --root %s", target, o.root)
		}
	} else if !inside(root, real) {
		root = real
		if !st.IsDir() {
			root = filepath.Dir(real)
		}
	}

	port := server.DefaultPort
	if o.hasPort {
		if port, err = strconv.Atoi(o.port); err != nil || port < 0 || port > 65535 {
			return usagef("invalid port: %s", o.port)
		}
	}

	srv, err := server.Start(server.Options{Root: root, Port: port, StrictPort: o.hasPort})
	if err != nil {
		return err
	}
	defer srv.Close()

	pageURL := srv.URL + "/"
	if rel, _ := filepath.Rel(root, real); rel != "." {
		segments := strings.Split(filepath.ToSlash(rel), "/")
		for i, seg := range segments {
			segments[i] = url.PathEscape(seg)
		}
		pageURL += strings.Join(segments, "/")
		if st.IsDir() {
			pageURL += "/"
		}
	}
	feedback := filepath.Join(store.DirName, "feedback.md")
	if rel, err := filepath.Rel(cwd, root); err == nil && rel != "." {
		feedback = filepath.Join(rel, feedback)
	}

	fmt.Fprintf(stdout, `
  inliner  %s

  root      %s
  feedback  %s

  Select text in the page to comment. Ctrl+C to stop.

`, pageURL, root, feedback)
	if !o.noOpen {
		openBrowser(pageURL)
	}

	<-ctx.Done()
	return nil
}

// openBrowser is best effort: the URL has been printed either way.
func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
