package services

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/urfave/cli"
)

// startGraceful serves h through g on a fresh 127.0.0.1 port and returns the
// address and the channel Serve's result lands in.
func startGraceful(t *testing.T, g *GracefulServer, h http.Handler) (string, <-chan error) {
	t.Helper()
	return startGracefulServer(t, g, &http.Server{Handler: h})
}

func startGracefulServer(t *testing.T, g *GracefulServer, srv *http.Server) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- g.Serve(srv, ln) }()
	return ln.Addr().String(), served
}

type getResult struct {
	body string
	err  error
}

// getDialer probes an idle connection after 1s. macOS sometimes ignores the
// reset that ends a cut stream (7 of 700 runs; Linux 0 of 800) and keeps the
// connection open until a keepalive probe draws a second reset: after 15s
// with Go's defaults, after ~1s with this dialer.
var getDialer = &net.Dialer{KeepAliveConfig: net.KeepAliveConfig{
	Enable: true, Idle: time.Second, Interval: time.Second, Count: 1,
}}

// get issues a GET on its own connection and reports the body once the
// response ends, whether completely or cut.
func get(url string) <-chan getResult {
	res := make(chan getResult, 1)
	go func() {
		cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: getDialer.DialContext}}
		resp, err := cl.Get(url)
		if err != nil {
			res <- getResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		res <- getResult{body: string(b), err: err}
	}()
	return res
}

// recvResult fails the test instead of blocking forever when a response
// never ends.
func recvResult(t *testing.T, res <-chan getResult) getResult {
	t.Helper()
	select {
	case got := <-res:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("response did not end within 2s")
		return getResult{}
	}
}

// closeWithin runs Close and fails the test if it has not returned within d,
// so an unbounded Close fails fast instead of hanging until go test's
// deadline. It returns how long Close took.
func closeWithin(t *testing.T, g *GracefulServer, d time.Duration) time.Duration {
	t.Helper()
	done := make(chan time.Duration, 1)
	go func() {
		begin := time.Now()
		g.Close()
		done <- time.Since(begin)
	}()
	select {
	case took := <-done:
		return took
	case <-time.After(d):
		t.Fatalf("Close did not return within %v", d)
		return 0
	}
}

func waitServed(t *testing.T, served <-chan error) {
	t.Helper()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v after Close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}

func assertRefused(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("server still accepts connections after Close")
	}
}

// captureLog records the standard logger's entries until the test ends.
func captureLog(t *testing.T) *test.Hook {
	t.Helper()
	hook := test.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	return hook
}

func logEntries(hook *test.Hook, level log.Level, msg string) []*log.Entry {
	var es []*log.Entry
	for _, e := range hook.AllEntries() {
		if e.Level == level && e.Message == msg {
			es = append(es, e)
		}
	}
	return es
}

// streamUntilCut writes chunks until the connection is cut.
func streamUntilCut(w http.ResponseWriter, r *http.Request) {
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		if _, err := io.WriteString(w, "chunk\n"); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
	}
}

// A pod receiving SIGTERM must finish the requests it is already serving:
// closing only the listener lets the process exit mid-response and the
// ingress answers 502.
func TestGracefulServerDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
		close(finished)
	})
	g := NewGracefulServer(5 * time.Second)
	addr, served := startGraceful(t, g, h)

	res := get("http://" + addr + "/slow")
	<-started
	closeWithin(t, g, 2*time.Second)

	// The process exits right after Close, so returning early is the bug even
	// if the request would have finished in a process that stayed up.
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the in-flight request finished")
	}
	got := recvResult(t, res)
	if got.err != nil || got.body != "done" {
		t.Fatalf("in-flight request was cut: body=%q err=%v", got.body, got.err)
	}
	waitServed(t, served)
	assertRefused(t, addr)
}

// A stream that outlives the timeout is cut at the deadline, so Close is
// bounded and the pod leaves within terminationGracePeriodSeconds.
func TestGracefulServerCutsStreamsAfterTimeout(t *testing.T) {
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		streamUntilCut(w, r)
	})
	const timeout = 200 * time.Millisecond
	g := NewGracefulServer(timeout)
	addr, served := startGraceful(t, g, h)

	res := get("http://" + addr + "/stream")
	<-started

	took := closeWithin(t, g, 2*time.Second)
	if took < timeout {
		t.Fatalf("Close returned after %v, before the %v drain timeout", took, timeout)
	}

	got := recvResult(t, res)
	if got.err == nil {
		t.Fatalf("stream ended cleanly, want it cut; got %d bytes", len(got.body))
	}
	if len(got.body) == 0 {
		t.Fatal("stream sent nothing before the cut")
	}
	waitServed(t, served)
	assertRefused(t, addr)
}

// Cutting a connection does not stop its handler: its deferred work (thp
// records its stats there) runs after the connection is gone. Close must wait
// for it, or that work runs into the dependencies the caller closes next.
func TestGracefulServerWaitsForCutHandlers(t *testing.T) {
	started := make(chan struct{})
	var unwound atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			time.Sleep(100 * time.Millisecond)
			unwound.Store(true)
		}()
		close(started)
		streamUntilCut(w, r)
	})
	g := NewGracefulServer(100 * time.Millisecond)
	addr, served := startGraceful(t, g, h)

	res := get("http://" + addr + "/stream")
	<-started
	closeWithin(t, g, 2*time.Second)
	if !unwound.Load() {
		t.Fatal("Close returned while the handler of a cut stream was still running")
	}
	if got := recvResult(t, res); got.err == nil {
		t.Fatal("stream ended cleanly, want it cut")
	}
	waitServed(t, served)
}

// A handler that ignores its cut connection must not hold Close past the
// grace period: the wait for cut handlers is bounded and says what is left.
func TestGracefulServerBoundsWaitForStuckHandlers(t *testing.T) {
	hook := captureLog(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		streamUntilCut(w, r)
		<-release
	})
	const timeout = 100 * time.Millisecond
	g := NewGracefulServer(timeout)
	g.unwind = 100 * time.Millisecond
	addr, served := startGraceful(t, g, h)

	res := get("http://" + addr + "/stream")
	<-started
	took := closeWithin(t, g, 2*time.Second)
	if took < timeout+g.unwind {
		t.Fatalf("Close took %v, want it to wait %v for the stuck handler", took, g.unwind)
	}
	warns := logEntries(hook, log.WarnLevel, "web handlers still running after closing their connections")
	if len(warns) != 1 || warns[0].Data["handlers"] != 1 {
		t.Fatalf("want one warning naming 1 stuck handler, got %v", warns)
	}
	recvResult(t, res)
	waitServed(t, served)
}

// Hijacked connections are outside the server's accounting, as with
// http.Server.Shutdown: Close must not wait for their handlers.
func TestGracefulServerDoesNotWaitForHijacked(t *testing.T) {
	hijacked := make(chan struct{})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		close(hijacked)
		<-release
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		streamUntilCut(w, r)
	})
	g := NewGracefulServer(100 * time.Millisecond)
	g.unwind = 5 * time.Second
	addr, served := startGraceful(t, g, mux)

	ws, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	if _, err := fmt.Fprint(ws, "GET /ws HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-hijacked
	// A stream past the timeout makes Close cut and wait for cut handlers.
	res := get("http://" + addr + "/stream")
	<-started

	closeWithin(t, g, 2*time.Second)
	recvResult(t, res)
	waitServed(t, served)
	// Nor is it reset with the cut streams: it is still open.
	_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := ws.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("hijacked connection after Close: %v, want it still open", err)
	}
}

// Serve takes over srv.ConnState for its accounting; a hook the caller set
// must keep firing.
func TestGracefulServerChainsConnState(t *testing.T) {
	var mu sync.Mutex
	var states []http.ConnState
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "done")
		}),
		ConnState: func(_ net.Conn, st http.ConnState) {
			mu.Lock()
			states = append(states, st)
			mu.Unlock()
		},
	}
	g := NewGracefulServer(5 * time.Second)
	addr, served := startGracefulServer(t, g, srv)

	if got := recvResult(t, get("http://"+addr+"/")); got.err != nil || got.body != "done" {
		t.Fatalf("body=%q err=%v", got.body, got.err)
	}
	closeWithin(t, g, 2*time.Second)
	waitServed(t, served)

	mu.Lock()
	defer mu.Unlock()
	seen := map[http.ConnState]bool{}
	for _, st := range states {
		seen[st] = true
	}
	if !seen[http.StateNew] || !seen[http.StateActive] {
		t.Fatalf("caller's ConnState saw %v, want new and active", states)
	}
}

// SIGTERM can arrive while the service is still starting: Close then runs
// before Serve, and a Serve that starts afterwards must not serve forever.
func TestGracefulServerCloseBeforeServe(t *testing.T) {
	g := NewGracefulServer(5 * time.Second)
	g.Close()

	addr, served := startGraceful(t, g, http.NotFoundHandler())
	waitServed(t, served)
	assertRefused(t, addr)
}

// Close can also land after Serve published the server but before
// http.Server.Serve registered the listener. The race test below almost
// never hits that window, so it is forced here.
func TestGracefulServerCloseBeforeListenerRegistered(t *testing.T) {
	g := NewGracefulServer(5 * time.Second)
	hooked := false
	var took time.Duration
	testHookBeforeServe = func() {
		hooked = true
		begin := time.Now()
		g.Close()
		took = time.Since(begin)
	}
	t.Cleanup(func() { testHookBeforeServe = nil })

	addr, served := startGraceful(t, g, http.NotFoundHandler())
	waitServed(t, served)
	if !hooked {
		t.Fatal("the hook did not run, the window was not exercised")
	}
	if took > 100*time.Millisecond {
		t.Fatalf("Close on a server with nothing to drain took %v", took)
	}
	assertRefused(t, addr)
}

// cs.Serve runs Serve in a goroutine while SIGTERM makes main call Close, with
// nothing ordering the two. Serve must return and stop accepting either way;
// -race reports unsynchronized state.
func TestGracefulServerCloseRacesServe(t *testing.T) {
	for i := 0; i < 50; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		g := NewGracefulServer(time.Second)
		start := make(chan struct{})
		served := make(chan error, 1)
		closed := make(chan struct{})
		go func() {
			<-start
			served <- g.Serve(&http.Server{Handler: http.NotFoundHandler()}, ln)
		}()
		go func() {
			<-start
			g.Close()
			close(closed)
		}()
		close(start)
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("Close did not return")
		}
		waitServed(t, served)
		assertRefused(t, ln.Addr().String())
	}
}

// Close runs from both an explicit call and a defer in callers, and cs.Serve
// may race it. Every Close must return only once the server is drained,
// otherwise a second caller would go on to close dependencies that in-flight
// handlers still use. The drain itself runs once.
func TestGracefulServerCloseTwice(t *testing.T) {
	hook := captureLog(t)

	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	g := NewGracefulServer(5 * time.Second)
	addr, served := startGraceful(t, g, h)

	res := get("http://" + addr + "/slow")
	<-started

	closed := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			g.Close()
			closed <- struct{}{}
		}()
	}
	select {
	case <-closed:
		t.Fatal("Close returned while a request was still in flight")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("Close did not return after the request finished")
		}
	}
	if got := recvResult(t, res); got.err != nil || got.body != "done" {
		t.Fatalf("in-flight request was cut: body=%q err=%v", got.body, got.err)
	}
	waitServed(t, served)

	if took := closeWithin(t, g, 2*time.Second); took > 100*time.Millisecond {
		t.Fatalf("Close on a closed server took %v", took)
	}

	if drains := len(logEntries(hook, log.InfoLevel, "closing web")); drains != 1 {
		t.Fatalf("drain ran %d times over three Close calls, want 1", drains)
	}
}

func TestShutdownTimeoutFlag(t *testing.T) {
	cases := []struct {
		name string
		env  string
		args []string
		want time.Duration
	}{
		{name: "default", want: 20 * time.Second},
		{name: "env", env: "45s", want: 45 * time.Second},
		{name: "flag", args: []string{"--shutdown-timeout", "1m"}, want: time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("WEB_SHUTDOWN_TIMEOUT", tc.env)
			} else {
				// Unset for the default case even if the runner exports it.
				t.Setenv("WEB_SHUTDOWN_TIMEOUT", "")
				_ = os.Unsetenv("WEB_SHUTDOWN_TIMEOUT")
			}
			got := runShutdownTimeout(t, RegisterShutdownFlags(nil), tc.args...)
			if got != tc.want {
				t.Fatalf("shutdown timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// A service that forgets RegisterShutdownFlags still starts and serves, and
// every rollout cuts in-flight requests at once again. Nothing else fails, so
// NewGracefulServer has to say it at startup.
func TestShutdownTimeoutUnregistered(t *testing.T) {
	t.Setenv("WEB_SHUTDOWN_TIMEOUT", "60s")
	hook := captureLog(t)
	const warning = "web shutdown timeout is not positive, in-flight requests will be cut at once; are the shutdown flags registered?"

	NewGracefulServer(20 * time.Second)
	if n := len(logEntries(hook, log.WarnLevel, warning)); n != 0 {
		t.Fatalf("warned %d times about a positive timeout", n)
	}

	got := runShutdownTimeout(t, nil)
	if got != 0 {
		t.Fatalf("shutdown timeout without the flags = %v, want 0", got)
	}
	NewGracefulServer(got)
	if n := len(logEntries(hook, log.WarnLevel, warning)); n != 1 {
		t.Fatalf("warned %d times about a zero timeout, want 1", n)
	}
}

func runShutdownTimeout(t *testing.T, flags []cli.Flag, args ...string) time.Duration {
	t.Helper()
	var got time.Duration
	app := cli.NewApp()
	app.Flags = flags
	app.Action = func(c *cli.Context) error {
		got = ShutdownTimeout(c)
		return nil
	}
	if err := app.Run(append([]string{"app"}, args...)); err != nil {
		t.Fatal(err)
	}
	return got
}

// serverWithSendBuffer is an http.Server whose connections get a fixed 1 MiB
// send buffer, so a client that does not read leaves a known, large tail of
// the response in the server's kernel.
func serverWithSendBuffer(h http.Handler) *http.Server {
	return &http.Server{
		Handler: h,
		ConnState: func(c net.Conn, st http.ConnState) {
			if st == http.StateNew {
				_ = c.(*net.TCPConn).SetWriteBuffer(1 << 20)
			}
		},
	}
}

// dialNotReading sends a GET on a raw connection with a small receive buffer
// and leaves the response unread until the caller reads it.
func dialNotReading(t *testing.T, addr, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.(*net.TCPConn).SetReadBuffer(16 << 10); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: test\r\n\r\n", path); err != nil {
		t.Fatal(err)
	}
	return c
}

// waitStalled waits until n stops growing: the handler's writes block on full
// socket buffers.
func waitStalled(t *testing.T, n *atomic.Int64) int64 {
	t.Helper()
	last := int64(-1)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		cur := n.Load()
		if cur > 0 && cur == last {
			return cur
		}
		last = cur
	}
	t.Fatal("the handler's writes never blocked")
	return 0
}

// A stream cut at the timeout must reach the client as a reset at once. A
// graceful close queues the FIN behind the unsent tail of the send buffer;
// in a pod the process exits and the network is gone before that goes out,
// and the client (thp, ingress) waits for its keepalive to notice: 150s with
// Go's defaults, measured in thp on 2026-10-07. Here the network stays up, so
// a FIN would deliver the whole tail and a clean EOF instead.
func TestGracefulServerResetsCutStreams(t *testing.T) {
	var written atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				return
			}
			written.Add(int64(len(buf)))
		}
	})
	g := NewGracefulServer(100 * time.Millisecond)
	addr, served := startGracefulServer(t, g, serverWithSendBuffer(h))

	c := dialNotReading(t, addr, "/stream")
	sent := waitStalled(t, &written)
	if sent < 512<<10 {
		t.Fatalf("only %d bytes backed up before the cut, the send buffer did not fill", sent)
	}
	closeWithin(t, g, 3*time.Second)

	cut := time.Now()
	_ = c.SetReadDeadline(cut.Add(time.Second))
	got, err := io.Copy(io.Discard, c)
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client read %d bytes and then %v within 1s of Close, want a connection reset", got, err)
	}
	if took := time.Since(cut); took > time.Second {
		t.Fatalf("reset reached the client %v after Close", took)
	}
	// The client may still read what already reached its own receive buffer;
	// what sat in the server's 1 MiB send buffer must be gone.
	if dropped := sent - got; dropped < 256<<10 {
		t.Fatalf("client read %d of the %d bytes written before the cut, the send buffer's tail was delivered", got, sent)
	}
	t.Logf("written before the cut %d, read by the client after it %d, reset after %v", sent, got, time.Since(cut))
	waitServed(t, served)
}

// Only connections still serving a request at the timeout are reset. One that
// finished its response during the drain closes as before, and its client
// still gets the whole body, even the part that sat unsent in the server's
// send buffer when a stream on another connection was reset.
func TestGracefulServerDeliversRequestsFinishedInDrain(t *testing.T) {
	const size = 256 << 10
	release := make(chan struct{})
	finished := make(chan struct{})
	started := make(chan struct{}, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = w.Write(make([]byte, size))
		close(finished)
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		streamUntilCut(w, r)
	})
	const timeout = 500 * time.Millisecond
	g := NewGracefulServer(timeout)
	addr, served := startGracefulServer(t, g, serverWithSendBuffer(mux))

	big := dialNotReading(t, addr, "/big")
	stream := get("http://" + addr + "/stream")
	<-started
	<-started

	closed := make(chan time.Duration, 1)
	go func() { closed <- closeWithin(t, g, 3*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case <-finished:
	case <-time.After(timeout / 2):
		t.Fatal("the /big handler did not finish well before the drain timeout")
	}
	if took := <-closed; took < timeout {
		t.Fatalf("Close took %v, the stream was not cut at the %v timeout", took, timeout)
	}
	if got := recvResult(t, stream); got.err == nil {
		t.Fatal("stream ended cleanly, want it cut")
	}

	_ = big.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(big), nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != size {
		t.Fatalf("finished response arrived as %d of %d bytes, err=%v", n, size, err)
	}
	waitServed(t, served)
}
