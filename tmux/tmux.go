// Package tmux types into and reads from a tmux pane safely.
//
// It wraps three tmux behaviours that break naive send-keys callers:
//   - keys sent to a pane in copy mode drive copy mode instead of the
//     program in the pane;
//   - a send-keys argument ending in ";" loses that ";" (tmux 3.6 reads it
//     as a command separator), and a lone ";" argument is parsed as one;
//   - Claude Code 2.1.332 collapses a single send-keys argument of 850 bytes
//     or more into a "[Pasted text #N]" placeholder (800 arrives intact), so
//     text goes in chunks well under that with a short pause between them.
//
// Long text can instead go in as one bracketed paste (Paste), which Claude
// Code 2.1.332 receives whole; it shows "[Pasted text #N]" in the box but the
// transcript holds every byte.
//
// Enter is always its own send-keys call.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/justinstimatze/gantline/screen"
)

// ErrPaneInMode is returned when the target pane is in copy mode or another
// tmux mode. Nothing is typed: cancelling the mode would pull a person out of
// scrollback they may be reading.
var ErrPaneInMode = errors.New("tmux: pane is in a mode (copy mode?); not typing")

// Server addresses one tmux server. The zero value is the default server.
type Server struct {
	// Socket is a tmux -L socket name. Empty means the default server.
	Socket string
	// Path is a tmux -S socket path, used when Socket is empty: the
	// server a session runs in, from $TMUX.
	Path string
}

// FromEnv is the server named by GANTLINE_TMUX_SOCKET (a -L name), else
// the one this process runs in ($TMUX), else the default server.
func FromEnv() Server {
	if name := os.Getenv("GANTLINE_TMUX_SOCKET"); name != "" {
		return Server{Socket: name}
	}
	if path, _, _ := strings.Cut(os.Getenv("TMUX"), ","); path != "" {
		return Server{Path: path}
	}
	return Server{}
}

// cmdTimeout bounds one tmux call, so a hung server can't hang a hook or
// a runner forever.
const cmdTimeout = 10 * time.Second

// command builds a tmux invocation against this server.
func (s Server) command(args ...string) (*exec.Cmd, context.CancelFunc) {
	switch {
	case s.Socket != "":
		args = append([]string{"-L", s.Socket}, args...)
	case s.Path != "":
		args = append([]string{"-S", s.Path}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	return exec.CommandContext(ctx, "tmux", args...), cancel
}

// Run executes a tmux command against the server and returns its stdout.
func (s Server) Run(args ...string) (string, error) {
	cmd, cancel := s.command(args...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// InMode reports whether pane is in copy mode or another tmux mode.
func (s Server) InMode(pane string) (bool, error) {
	out, err := s.Run("display-message", "-p", "-t", pane, "#{pane_in_mode}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "0", nil
}

// Capture returns the visible contents of pane as plain text, with wrapped
// lines joined.
//
// It captures with escapes and reduces them with screen.Plain, so a dimmed
// prompt suggestion in the input box reads as an empty box.
func (s Server) Capture(pane string) (string, error) {
	c, err := s.Run("capture-pane", "-p", "-e", "-J", "-t", pane)
	if err != nil {
		return "", err
	}
	return screen.Plain(c), nil
}

// CaptureScroll is Capture plus up to scrollback lines of history above the
// visible screen, with terminal escapes kept when escapes is set.
func (s Server) CaptureScroll(pane string, scrollback int, escapes bool) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", pane}
	if scrollback > 0 {
		args = append(args, "-S", fmt.Sprint(-scrollback))
	}
	if escapes {
		args = append(args, "-e")
	}
	return s.Run(args...)
}

// TypeOptions controls chunked typing. Zero fields take the defaults.
type TypeOptions struct {
	// ChunkBytes caps each send-keys argument. Default 256, under a third of
	// the size where Claude Code 2.1.332 starts collapsing input into a paste
	// placeholder (cmd/chunkprobe measures it).
	ChunkBytes int
	// Gap is the pause between chunks. Zero means the default, 5ms.
	Gap time.Duration
}

func (o TypeOptions) withDefaults() TypeOptions {
	if o.ChunkBytes <= 0 {
		o.ChunkBytes = 256
	}
	if o.Gap <= 0 {
		o.Gap = 5 * time.Millisecond
	}
	return o
}

// Type sends text to pane literally, without pressing Enter. It refuses with
// ErrPaneInMode if the pane is in a mode, checking before every chunk.
func (s Server) Type(pane, text string, opt TypeOptions) error {
	opt = opt.withDefaults()
	args := SendArgs(text, opt.ChunkBytes)
	for i, a := range args {
		inMode, err := s.InMode(pane)
		if err != nil {
			return err
		}
		if inMode {
			return ErrPaneInMode
		}
		if _, err := s.Run("send-keys", "-t", pane, "-l", "--", a); err != nil {
			return err
		}
		if i < len(args)-1 {
			time.Sleep(opt.Gap)
		}
	}
	return nil
}

var pasteSeq atomic.Int64

// Paste puts text into pane as one bracketed paste, without pressing Enter.
// The text goes through a paste buffer named for this process, which
// paste-buffer -d deletes, so a concurrent paste or a person's own buffers
// are untouched. -p asks tmux to wrap it in bracketed-paste markers when the
// program has turned that mode on, as Claude Code does. It refuses with
// ErrPaneInMode if the pane is in a mode.
func (s Server) Paste(pane, text string) error {
	inMode, err := s.InMode(pane)
	if err != nil {
		return err
	}
	if inMode {
		return ErrPaneInMode
	}
	name := fmt.Sprintf("gantline-%d-%d", os.Getpid(), pasteSeq.Add(1))
	cmd, cancel := s.command("load-buffer", "-b", name, "-")
	defer cancel()
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, err = s.Run("paste-buffer", "-p", "-d", "-b", name, "-t", pane)
	return err
}

// Enter presses Enter in pane, refusing if the pane is in a mode.
func (s Server) Enter(pane string) error {
	inMode, err := s.InMode(pane)
	if err != nil {
		return err
	}
	if inMode {
		return ErrPaneInMode
	}
	_, err = s.Run("send-keys", "-t", pane, "Enter")
	return err
}

// Submit types text and then presses Enter as a separate call.
func (s Server) Submit(pane, text string, opt TypeOptions) error {
	if err := s.Type(pane, text, opt); err != nil {
		return err
	}
	time.Sleep(opt.withDefaults().Gap)
	return s.Enter(pane)
}

// SendArgs splits text into send-keys -l arguments of at most chunkBytes
// bytes, on rune boundaries, such that tmux delivers every byte literally.
// A trailing run of ";" is cut off each chunk and each ";" is sent as its own
// "\;" argument, which tmux delivers as a literal ";".
func SendArgs(text string, chunkBytes int) []string {
	if chunkBytes < utf8.UTFMax {
		chunkBytes = utf8.UTFMax
	}
	var out []string
	for len(text) > 0 {
		n := 0
		for n < len(text) {
			_, size := utf8.DecodeRuneInString(text[n:])
			if n+size > chunkBytes {
				break
			}
			n += size
		}
		chunk := text[:n]
		text = text[n:]
		body := strings.TrimRight(chunk, ";")
		if body != "" {
			out = append(out, body)
		}
		for range len(chunk) - len(body) {
			out = append(out, `\;`)
		}
	}
	return out
}
