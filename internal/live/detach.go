package live

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

// readyFDEnv names the inherited descriptor a detached child reports on.
const readyFDEnv = "LINO_READY_FD"

// ReadyTimeout bounds how long a detached start waits for the index.
var ReadyTimeout = 5 * time.Minute

// Executable returns the binary a detached start re-executes.
var Executable = os.Executable

// maxLog is the size above which the process log is truncated on start.
const maxLog = 8 << 20

// readyMsg is the single JSON line a detached child writes when it is ready
// or has failed to start.
type readyMsg struct {
	RunData
	Message string          `json:"message,omitempty"`
	Outcome outcome.Outcome `json:"outcome,omitempty"`
	Error   string          `json:"error,omitempty"`
	Hint    string          `json:"hint,omitempty"`
}

// LogPath returns the log file of process id in reg.
func LogPath(reg *registry.Registry, id string) string {
	return filepath.Join(reg.Dir, id+".log")
}

// existing returns the live entry for root, if any.
func existing(reg *registry.Registry, root string) (RunData, bool) {
	e, err := reg.Read(registry.IDFor(root))
	if err != nil || registry.Check(e) != registry.Live {
		return RunData{}, false
	}
	return RunData{ID: e.ID, Name: e.Name, Root: e.Root, PID: e.PID, Existing: true}, true
}

// startDetached starts a background process for req.Dir and waits until its
// index is ready. Concurrent calls for one root start exactly one process:
// the start is serialised by a per-root lock, and whoever gets it second finds
// the first one live.
func startDetached(ctx context.Context, reg *registry.Registry, req RunRequest) (output.Result, error) {
	root, ok, err := reg.FindRoot(dirOrCwd(req.Dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return output.Result{}, outcome.New(outcome.NotFound, "no such directory: %s", req.Dir)
		}
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	if !ok {
		d := dirOrCwd(req.Dir)
		return output.Result{}, outcome.New(outcome.NotRunning, "%s is not inside an initialised lino root", d).
			WithHint("lino init " + d)
	}
	if req.Name != "" && !registry.ValidName(req.Name) {
		return output.Result{}, outcome.New(outcome.Usage, "invalid name %q: use letters, digits, '.', '_' or '-'", req.Name)
	}
	id := registry.IDFor(root)
	if err := os.MkdirAll(reg.Dir, 0o700); err != nil {
		return output.Result{}, outcome.Wrap(outcome.Internal, err, "")
	}
	unlock, err := lockFile(ctx, filepath.Join(reg.Dir, id+".start.lock"))
	if err != nil {
		return output.Result{}, err
	}
	defer unlock()
	if d, ok := existing(reg, root); ok {
		return alreadyRunning(reg, d, req.Name)
	}
	msg, err := spawn(ctx, reg, root, id, req)
	if err != nil {
		if outcome.Is(err, outcome.LiveExists) {
			if d, ok := existing(reg, root); ok {
				return alreadyRunning(reg, d, req.Name)
			}
		}
		return output.Result{}, err
	}
	if msg.CreatedLinoignore {
		msg.Message += "\n" + CreatedLinoignoreNote
	}
	return output.Result{Data: msg.RunData, Message: msg.Message}, nil
}

func spawn(ctx context.Context, reg *registry.Registry, root, id string, req RunRequest) (readyMsg, error) {
	exe, err := Executable()
	if err != nil {
		return readyMsg{}, outcome.Wrap(outcome.Internal, err, "locate lino binary: "+err.Error())
	}
	logPath := LogPath(reg, id)
	if st, err := os.Stat(logPath); err == nil && st.Size() > maxLog {
		os.Truncate(logPath, 0)
	}
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return readyMsg{}, outcome.Wrap(outcome.Internal, err, "open log: "+err.Error())
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return readyMsg{}, outcome.Wrap(outcome.Internal, err, "")
	}
	defer devnull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		return readyMsg{}, outcome.Wrap(outcome.Internal, err, "")
	}
	defer pr.Close()

	args := []string{"run", root, "--foreground"}
	if req.Name != "" {
		args = append(args, "--name", req.Name)
	}
	if req.Idle != "" {
		args = append(args, "--idle", req.Idle)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), readyFDEnv+"=3")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	fmt.Fprintf(logf, "--- %s start %s\n", time.Now().Format(time.RFC3339), root)
	err = cmd.Start()
	pw.Close()
	if err != nil {
		return readyMsg{}, outcome.Wrap(outcome.Internal, err, "start process: "+err.Error())
	}

	type result struct {
		msg readyMsg
		err error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(pr).ReadBytes('\n')
		if err != nil && len(line) == 0 {
			ch <- result{err: err}
			return
		}
		var m readyMsg
		err = json.Unmarshal(line, &m)
		ch <- result{msg: m, err: err}
	}()
	timer := time.NewTimer(ReadyTimeout)
	defer timer.Stop()
	var r result
	select {
	case r = <-ch:
	case <-timer.C:
		cmd.Process.Kill()
		cmd.Wait()
		return readyMsg{}, outcome.New(outcome.Internal, "process did not become ready within %s", ReadyTimeout).
			WithHint("see " + logPath)
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		return readyMsg{}, ctx.Err()
	}
	if r.err != nil {
		// The child exited (or closed the pipe) without reporting.
		cmd.Wait()
		return readyMsg{}, outcome.New(outcome.Internal, "process exited during startup").WithHint("see " + logPath)
	}
	if r.msg.Error != "" {
		cmd.Wait()
		o := r.msg.Outcome
		if o == "" || o.Success() {
			o = outcome.Internal
		}
		e := outcome.New(o, "%s", r.msg.Error)
		if r.msg.Hint != "" {
			e.WithHint(r.msg.Hint)
		}
		return readyMsg{}, e
	}
	cmd.Process.Release()
	return r.msg, nil
}

// readyReporter returns the pipe a detached parent waits on, or nil in a
// plain foreground run. The variable is cleared so it does not leak further.
func readyReporter() *os.File {
	v := os.Getenv(readyFDEnv)
	if v == "" {
		return nil
	}
	os.Unsetenv(readyFDEnv)
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return nil
	}
	return os.NewFile(uintptr(fd), "lino-ready")
}

// reportReady writes the ready (or failure) line to f and closes it.
func reportReady(f *os.File, d RunData, message string, startErr error) {
	if f == nil {
		return
	}
	defer f.Close()
	m := readyMsg{RunData: d, Message: message}
	if startErr != nil {
		m = readyMsg{Outcome: outcome.Of(startErr), Error: startErr.Error()}
		if e, ok := outcome.As(startErr); ok {
			m.Hint = e.Hint
		}
	}
	b, _ := json.Marshal(m)
	f.Write(append(b, '\n'))
}

// lockFile takes an exclusive flock on path, polling so ctx can cancel.
func lockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "start lock: "+err.Error())
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, outcome.Wrap(outcome.Internal, err, "start lock: "+err.Error())
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
