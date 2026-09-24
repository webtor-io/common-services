package services

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const (
	shutdownTimeoutFlag = "shutdown-timeout"
)

// unwindTimeout bounds how long Close waits, after cutting the connections
// still open at the drain timeout, for their handlers to return. Unwinding
// takes milliseconds; a handler still running after this ignores both its
// closed connection and its cancelled context.
const unwindTimeout = 2 * time.Second

// testHookBeforeServe runs between publishing the server and srv.Serve, so a
// test can call Close exactly in that window.
var testHookBeforeServe func()

// RegisterShutdownFlags registers cli flags for GracefulServer
func RegisterShutdownFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.DurationFlag{
			Name:   shutdownTimeoutFlag,
			Usage:  "how long to let in-flight requests finish on SIGTERM; keep below terminationGracePeriodSeconds minus the preStop sleep",
			Value:  20 * time.Second,
			EnvVar: "WEB_SHUTDOWN_TIMEOUT",
		},
	)
}

// ShutdownTimeout returns the drain timeout for NewGracefulServer. It returns
// 0, which cuts in-flight requests at once, when RegisterShutdownFlags was not
// applied to this context's flags; NewGracefulServer warns about that.
func ShutdownTimeout(c *cli.Context) time.Duration {
	return c.Duration(shutdownTimeoutFlag)
}

// GracefulServer drains an http.Server on shutdown. Closing only the listener
// lets the process exit mid-response on SIGTERM, and the ingress answers 502
// on every request a terminating pod is still serving.
//
// Close must run before the dependencies the handlers use (Redis, NATS, DB
// clients) are closed. Defers run in reverse, so either defer Close after
// all of them or call it explicitly once cs.Serve returns. Close returns only
// after the handlers are done, including those of streams it cut at the
// timeout, so their deferred work does not run into closed dependencies. Two
// exceptions: handlers of hijacked connections (WebSockets), and cut handlers
// still running 2s after the cut, which Close logs and leaves behind.
//
// The timeout is the one given to NewGracefulServer, usually
// ShutdownTimeout(c) (WEB_SHUTDOWN_TIMEOUT). It is meant for the service's
// main web server: the log lines say "web". One GracefulServer serves one
// http.Server; use NewGracefulServer.
type GracefulServer struct {
	timeout time.Duration
	unwind  time.Duration
	once    sync.Once
	mu      sync.Mutex
	srv     *http.Server
	closed  bool
	active  map[net.Conn]struct{}
	idle    chan struct{}
}

// NewGracefulServer initializes GracefulServer that waits up to timeout for
// in-flight requests on Close
func NewGracefulServer(timeout time.Duration) *GracefulServer {
	if timeout <= 0 {
		log.WithField("timeout", timeout).Warn("web shutdown timeout is not positive, in-flight requests will be cut at once; are the shutdown flags registered?")
	}
	return &GracefulServer{
		timeout: timeout,
		unwind:  unwindTimeout,
		active:  map[net.Conn]struct{}{},
	}
}

// Serve serves srv on ln until Close. It returns nil after Close, including
// when Close already ran: then ln is closed and nothing is served. It chains
// srv.ConnState to track in-flight requests.
func (s *GracefulServer) Serve(srv *http.Server, ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	prev := srv.ConnState
	srv.ConnState = func(c net.Conn, st http.ConnState) {
		s.trackConn(c, st)
		if prev != nil {
			prev(c, st)
		}
	}
	s.srv = srv
	s.mu.Unlock()
	if testHookBeforeServe != nil {
		testHookBeforeServe()
	}
	// A Close between the unlock and srv.Serve is safe: Serve on a server
	// that is shutting down returns ErrServerClosed and closes ln.
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// trackConn keeps the set of connections serving a request: StateActive fires
// before the handler runs, and the next state (idle, closed, hijacked) after
// it has returned or taken the connection over.
func (s *GracefulServer) trackConn(c net.Conn, st http.ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st == http.StateActive {
		s.active[c] = struct{}{}
		return
	}
	delete(s.active, c)
	if len(s.active) == 0 && s.idle != nil {
		close(s.idle)
		s.idle = nil
	}
}

// Close stops accepting, closes idle keep-alive connections and waits for
// in-flight requests up to the timeout, then cuts whatever is still open
// (long-lived streams) and waits up to 2s for the cut handlers to return.
// Hijacked connections (WebSockets) are neither waited for nor cut, as with
// http.Server.Shutdown. It is safe to call more than once and before Serve;
// every call returns only after the drain is over.
func (s *GracefulServer) Close() {
	s.once.Do(s.close)
}

func (s *GracefulServer) close() {
	s.mu.Lock()
	s.closed = true
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	log.WithField("timeout", s.timeout).Info("closing web")
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Warn("web shutdown timed out, closing remaining connections")
		_ = srv.Close()
		s.waitUnwind()
	}
	log.Info("web closed")
}

// waitUnwind waits for the handlers of the connections srv.Close cut. Closing
// a connection does not stop its handler, it only makes its writes fail and
// cancels its context.
func (s *GracefulServer) waitUnwind() {
	s.mu.Lock()
	var idle chan struct{}
	if len(s.active) > 0 {
		idle = make(chan struct{})
		s.idle = idle
	}
	s.mu.Unlock()
	if idle == nil {
		return
	}
	t := time.NewTimer(s.unwind)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
		s.mu.Lock()
		n := len(s.active)
		s.mu.Unlock()
		log.WithField("handlers", n).Warn("web handlers still running after closing their connections")
	}
}
