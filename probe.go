package services

import (
	"fmt"
	"net"
	"net/http"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

// Probe provides simple HTTP-service for Kubernetes liveness and readiness checking
type Probe struct {
	host string
	port int
	ln   net.Listener
}

const (
	probeHostFlag = "probe-host"
	probePortFlag = "probe-port"
	probeUseFlag  = "use-probe"
)

// RegisterProbeFlags registers cli flags for Probe
func RegisterProbeFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   probeHostFlag,
			Usage:  "probe listening host",
			Value:  "",
			EnvVar: "PROBE_HOST",
		},
		cli.IntFlag{
			Name:   probePortFlag,
			Usage:  "probe listening port",
			Value:  8081,
			EnvVar: "PROBE_PORT",
		},
		cli.BoolTFlag{
			Name:   probeUseFlag,
			Usage:  "enable probe",
			EnvVar: "USE_PROBE",
		},
	)
}

// NewProbe initializes new Probe instance
func NewProbe(c *cli.Context) *Probe {
	if !c.BoolT(probeUseFlag) {
		return nil
	}
	return &Probe{
		host: c.String(probeHostFlag),
		port: c.Int(probePortFlag),
	}
}

// Serve serves Probe web service
func (s *Probe) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "failed to probe listen to tcp connection")
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/liveness", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	log.Infof("serving probe at %v", addr)
	return http.Serve(ln, mux)
}

// Close closes Probe web service
func (s *Probe) Close() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
}
