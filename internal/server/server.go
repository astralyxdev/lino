package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/proto"
)

// Handler runs one command. It is the same function the CLI uses in direct mode.
type Handler func(ctx context.Context, req *proto.Request) (output.Result, error)

// MutatingCommands are serialised through the single writer by default.
var MutatingCommands = map[string]bool{
	"edit": true, "insert": true, "delete": true, "replace": true, "write": true,
	"mv": true, "rm": true, "rollback": true, "index": true,
}

// DefaultMutating reports whether req is in MutatingCommands.
func DefaultMutating(req *proto.Request) bool { return MutatingCommands[req.Command] }

// Server accepts JSON-lines requests on a unix socket. Mutations run one at a
// time on a single writer goroutine; other commands run concurrently on their
// connection's goroutine.
type Server struct {
	Handler  Handler
	Mutating func(*proto.Request) bool // nil means DefaultMutating
	// OnShutdown runs once after all connections and the writer have finished.
	OnShutdown func()

	initOnce sync.Once
	ctx      context.Context // cancelled when shutdown starts; mutations ignore it
	cancel   context.CancelFunc
	writes   chan job
	writerWG sync.WaitGroup

	mu       sync.Mutex
	ln       net.Listener
	conns    map[net.Conn]struct{}
	connWG   sync.WaitGroup
	closing  atomic.Bool
	done     chan struct{}
	shutOnce sync.Once
	lastUsed atomic.Int64
	inFlight atomic.Int64
}

type job struct {
	ctx   context.Context
	req   *proto.Request
	reply chan *proto.Response
}

func (s *Server) init() {
	s.initOnce.Do(func() {
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.writes = make(chan job)
		s.conns = map[net.Conn]struct{}{}
		s.done = make(chan struct{})
		s.lastUsed.Store(time.Now().UnixNano())
		s.writerWG.Add(1)
		go s.writer()
	})
}

// Listen creates a unix socket at path with mode 0600, in a 0700 directory.
// A leftover socket nobody accepts on is removed; a live one is refused.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 300*time.Millisecond); err == nil {
			c.Close()
			return nil, outcome.New(outcome.LiveExists, "a process already listens on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve accepts connections until Shutdown. It returns nil after Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	s.init()
	s.mu.Lock()
	if s.closing.Load() {
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	s.ln = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closing.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.mu.Lock()
		if s.closing.Load() {
			s.mu.Unlock()
			c.Close()
			return nil
		}
		s.conns[c] = struct{}{}
		s.connWG.Add(1)
		s.mu.Unlock()
		go s.serveConn(c)
	}
}

// LastActivity is when the last request arrived or finished (or the server
// started).
func (s *Server) LastActivity() time.Time {
	s.init()
	return time.Unix(0, s.lastUsed.Load())
}

// InFlight is the number of requests being handled now, including waiting
// reads such as changes --wait.
func (s *Server) InFlight() int {
	return int(s.inFlight.Load())
}

// Done is closed once Shutdown has finished.
func (s *Server) Done() <-chan struct{} {
	s.init()
	return s.done
}

// Shutdown stops accepting, lets in-flight requests finish, then closes every
// connection, stops the writer and runs OnShutdown. Waiting reads (such as
// changes --wait) see their context cancelled; mutations are never cut short.
// When ctx expires first, remaining connections are closed forcibly. A handler
// that wants to stop the server must call Shutdown in a new goroutine.
func (s *Server) Shutdown(ctx context.Context) error {
	s.init()
	first := false
	s.shutOnce.Do(func() { first = true })
	if !first {
		select {
		case <-s.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.closing.Store(true)
	if s.ln != nil {
		s.ln.Close()
	}
	for c := range s.conns {
		c.SetReadDeadline(time.Now()) // unblocks idle readers only
	}
	s.mu.Unlock()
	s.cancel()

	drained := make(chan struct{})
	go func() { s.connWG.Wait(); close(drained) }()
	var err error
	select {
	case <-drained:
	case <-ctx.Done():
		err = ctx.Err()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		<-drained
	}
	close(s.writes)
	s.writerWG.Wait()
	if s.OnShutdown != nil {
		s.OnShutdown()
	}
	close(s.done)
	return err
}

func (s *Server) serveConn(c net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
		s.connWG.Done()
	}()
	r := proto.NewReader(c)
	for !s.closing.Load() {
		req, err := r.ReadRequest()
		if err != nil {
			if errors.Is(err, io.EOF) || s.closing.Load() {
				return
			}
			var oe *outcome.Error
			if !errors.As(err, &oe) {
				return // transport error or partial line: nothing left to answer
			}
			if proto.WriteResponse(c, proto.ErrorResponse(err, false)) != nil || oe.Outcome == outcome.Refused {
				return // an oversized message leaves the stream unsynchronised
			}
			continue
		}
		s.lastUsed.Store(time.Now().UnixNano())
		s.inFlight.Add(1)
		resp := s.dispatch(req)
		s.lastUsed.Store(time.Now().UnixNano())
		s.inFlight.Add(-1)
		if proto.WriteResponse(c, resp) != nil {
			return
		}
	}
}

func (s *Server) dispatch(req *proto.Request) *proto.Response {
	mutating := DefaultMutating
	if s.Mutating != nil {
		mutating = s.Mutating
	}
	if !mutating(req) {
		return s.run(s.ctx, req)
	}
	j := job{ctx: context.WithoutCancel(s.ctx), req: req, reply: make(chan *proto.Response, 1)}
	s.writes <- j
	return <-j.reply
}

func (s *Server) writer() {
	defer s.writerWG.Done()
	for j := range s.writes {
		j.reply <- s.run(j.ctx, j.req)
	}
}

func (s *Server) run(ctx context.Context, req *proto.Request) (resp *proto.Response) {
	defer func() {
		if p := recover(); p != nil {
			resp = proto.ErrorResponse(outcome.New(outcome.Internal, "panic in %s: %v", req.Command, p), req.JSON)
		}
	}()
	if s.Handler == nil {
		return proto.ErrorResponse(outcome.New(outcome.Internal, "no handler"), req.JSON)
	}
	res, err := s.Handler(ctx, req)
	if err != nil {
		return proto.ErrorResponse(err, req.JSON)
	}
	resp, err = proto.NewResponse(res, req.JSON)
	if err != nil {
		return proto.ErrorResponse(fmt.Errorf("encode %s: %w", req.Command, err), req.JSON)
	}
	return resp
}
