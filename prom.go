package services

import (
	"fmt"
	"net"
	"net/http"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const (
	promHostFlag = "prom-host"
	promPortFlag = "prom-port"
	promUseFlag  = "use-prom"
)

type Prom struct {
	host string
	port int
	ln   net.Listener
}

func RegisterPromFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   promHostFlag,
			Usage:  "prometheus metrics listening host",
			Value:  "",
			EnvVar: "PROM_HOST",
		},
		cli.IntFlag{
			Name:   promPortFlag,
			Usage:  "prometheus metrics listening port",
			Value:  8083,
			EnvVar: "PROM_PORT",
		},
		cli.BoolTFlag{
			Name:   promUseFlag,
			Usage:  "use prometheus metrics",
			EnvVar: "USE_PROM",
		},
	)
}

func NewProm(c *cli.Context) *Prom {
	if !c.BoolT(promUseFlag) {
		return nil
	}
	return &Prom{
		host: c.String(promHostFlag),
		port: c.Int(promPortFlag),
	}
}

func (s *Prom) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "failed to listen to tcp connection")
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	logrus.Infof("serving Prometheus metrics at %v", addr)
	return http.Serve(ln, mux)
}

func (s *Prom) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
}
