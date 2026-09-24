# common-services
Collection of commonly used services at webtor.io

## Probe
Generates standard liveness and readiness probe endpoints for kubernetes

## Serve
Runs simultaneously multiple services in goroutines

## GracefulServer
Drains an `http.Server` on SIGTERM: stops accepting, waits for in-flight requests
up to `--shutdown-timeout` / `WEB_SHUTDOWN_TIMEOUT` (default 20s), then cuts what is
still open and waits up to 2s more for the handlers of the cut connections to
return. Closing only the listener lets the process exit mid-response, and the
ingress answers 502. Set `terminationGracePeriodSeconds` to at least the preStop
sleep + the timeout + 2s, plus whatever the rest of the shutdown needs.

`Close` returns only after the handlers are done, cut ones included, except
handlers of hijacked connections (WebSockets) and handlers still running 2s after
the cut (logged as a warning). It must run before the dependencies the handlers
use are closed, so call it right after `cs.Serve` (`serve.Serve()`) returns
instead of leaving it to defer order:

```golang
app.Flags = cs.RegisterShutdownFlags(app.Flags)

// in the web service
gs := cs.NewGracefulServer(cs.ShutdownTimeout(c))
// Serve:  return gs.Serve(&http.Server{Handler: mux}, ln)
// Close:  gs.Close()

// in run()
err := serve.Serve()
web.Close() // drain before the deferred Redis/NATS/... closes
```

Without `RegisterShutdownFlags` the timeout is 0 and every in-flight request is
cut at once; `NewGracefulServer` logs a warning at startup when that happens.

## Example usage

```golang
package main

import (
	cs "github.com/webtor-io/common-services"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	s "github.com/webtor-io/torrent-http-proxy/services"
)

func configure(app *cli.App) {
	app.Flags = []cli.Flag{}

	s.RegisterWebFlags(app)
	cs.RegisterProbeFlags(app)

	app.Action = run
}

func run(c *cli.Context) error {
	// Setting ProbeService
	probe := cs.NewProbe(c)
	defer probe.Close()

	// Setting WebService
	web := s.NewWeb(c)
	defer web.Close()

	// Setting ServeService
	serve := cs.NewServe(probe, web)

	// And SERVE!
	err := serve.Serve()
	if err != nil {
		log.WithError(err).Error("Got serve error")
	}
	return err
}
