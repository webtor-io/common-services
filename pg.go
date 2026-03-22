package services

import (
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/go-pg/pg/v10"
	"github.com/urfave/cli"
)

const (
	pgHostFlag     = "postgres-host"
	pgPortFlag     = "postgres-port"
	pgUserFlag     = "postgres-user"
	pgPasswordFlag = "postgres-password"
	pgDatabaseFlag = "postgres-database"
	pgSSLFlag          = "postgres-ssl"
	pgPoolSizeFlag     = "postgres-pool-size"
	pgMinIdleConnsFlag = "postgres-min-idle-conns"
	pgMaxConnAgeFlag   = "postgres-max-conn-age"
	pgIdleTimeoutFlag  = "postgres-idle-timeout"
)

func RegisterPGFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   pgHostFlag,
			Usage:  "postgres host",
			Value:  "",
			EnvVar: "PG_HOST",
		},
		cli.IntFlag{
			Name:   pgPortFlag,
			Usage:  "postgres port",
			Value:  5432,
			EnvVar: "PG_PORT",
		},
		cli.StringFlag{
			Name:   pgUserFlag,
			Usage:  "postgres user",
			Value:  "",
			EnvVar: "PG_USER",
		},
		cli.StringFlag{
			Name:   pgPasswordFlag,
			Usage:  "postgres password",
			Value:  "",
			EnvVar: "PG_PASSWORD",
		},
		cli.StringFlag{
			Name:   pgDatabaseFlag,
			Usage:  "postgres database",
			Value:  "",
			EnvVar: "PG_DATABASE",
		},
		cli.BoolFlag{
			Name:   pgSSLFlag,
			Usage:  "postgres ssl",
			EnvVar: "PG_SSL",
		},
		cli.IntFlag{
			Name:   pgPoolSizeFlag,
			Usage:  "postgres pool size",
			Value:  5,
			EnvVar: "PG_POOL_SIZE",
		},
		cli.IntFlag{
			Name:   pgMinIdleConnsFlag,
			Usage:  "postgres min idle connections",
			Value:  1,
			EnvVar: "PG_MIN_IDLE_CONNS",
		},
		cli.StringFlag{
			Name:   pgMaxConnAgeFlag,
			Usage:  "postgres max connection age",
			Value:  "30m",
			EnvVar: "PG_MAX_CONN_AGE",
		},
		cli.StringFlag{
			Name:   pgIdleTimeoutFlag,
			Usage:  "postgres idle timeout",
			Value:  "5m",
			EnvVar: "PG_IDLE_TIMEOUT",
		},
	)
}

type PG struct {
	host         string
	port         int
	user         string
	password     string
	database     string
	ssl          bool
	poolSize     int
	minIdleConns int
	maxConnAge   time.Duration
	idleTimeout  time.Duration
	db           *pg.DB
	mux          sync.Mutex
	inited       bool
}

func NewPG(c *cli.Context) *PG {
	maxConnAge, _ := time.ParseDuration(c.String(pgMaxConnAgeFlag))
	if maxConnAge == 0 {
		maxConnAge = 30 * time.Minute
	}
	idleTimeout, _ := time.ParseDuration(c.String(pgIdleTimeoutFlag))
	if idleTimeout == 0 {
		idleTimeout = 5 * time.Minute
	}
	return &PG{
		host:         c.String(pgHostFlag),
		port:         c.Int(pgPortFlag),
		user:         c.String(pgUserFlag),
		password:     c.String(pgPasswordFlag),
		database:     c.String(pgDatabaseFlag),
		ssl:          c.Bool(pgSSLFlag),
		poolSize:     c.Int(pgPoolSizeFlag),
		minIdleConns: c.Int(pgMinIdleConnsFlag),
		maxConnAge:   maxConnAge,
		idleTimeout:  idleTimeout,
	}
}

func (s *PG) get() *pg.DB {
	if s.host == "" {
		return nil
	}
	opts := &pg.Options{}
	opts.Addr = fmt.Sprintf("%v:%v", s.host, s.port)
	opts.User = s.user
	opts.Password = s.password
	opts.Database = s.database
	opts.PoolSize = s.poolSize
	opts.MinIdleConns = s.minIdleConns
	opts.MaxConnAge = s.maxConnAge
	opts.IdleTimeout = s.idleTimeout
	if s.ssl {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	return pg.Connect(opts)
}

func (s *PG) Get() *pg.DB {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.inited {
		return s.db
	}
	s.db = s.get()
	s.inited = true
	return s.db
}

func (s *PG) Close() {
	if s.db != nil {
		s.db.Close()
	}
}
