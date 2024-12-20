package services

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// Serve serves multible Servables at ones, handles errors and system signals
type Serve struct {
	servables []Servable
}

// Servable serves something
type Servable interface {
	Serve() error
}

// NewServe initializes Serve
func NewServe(s ...Servable) *Serve {
	return &Serve{servables: s}
}

// Serve serves multiple Servables
func (s *Serve) Serve() error {

	serveError := make(chan error, 1)

	for _, ss := range s.servables {
		if ss == nil {
			continue
		}
		go func(sss Servable) {
			err := sss.Serve()
			serveError <- err
		}(ss)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigs:
		log.WithField("signal", sig).Info("got syscall")
	case err := <-serveError:
		if err != nil {
			return errors.Wrap(err, "got serve error")
		}
	}
	log.Info("shutting down... at last!")
	return nil
}
