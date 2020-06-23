package services

import (
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

// S3Client makes AWS SDK S3 Client from cli and environment variables
type S3Client struct {
	accessKeyID     string
	secretAccessKey string
	endpoint        string
	region          string
	s3              *s3.S3
	mux             sync.Mutex
	err             error
	inited          bool
}

const (
	awsAccessKeyID     = "aws-access-key-id"
	awsSecretAccessKey = "aws-secret-access-key"
	awsEndpoint        = "aws-endpoint"
	awsRegion          = "aws-region"
)

// RegisterS3ClientFlags registers cli flags for S3 client
func RegisterS3ClientFlags(c *cli.App) {
	c.Flags = append(c.Flags, cli.StringFlag{
		Name:   awsAccessKeyID,
		Usage:  "AWS Access Key ID",
		Value:  "",
		EnvVar: "awsAccessKeyID",
	})
	c.Flags = append(c.Flags, cli.StringFlag{
		Name:   awsSecretAccessKey,
		Usage:  "AWS Secret Access Key",
		Value:  "",
		EnvVar: "awsSecretAccessKey",
	})
	c.Flags = append(c.Flags, cli.StringFlag{
		Name:   awsEndpoint,
		Usage:  "AWS Endpoint",
		Value:  "",
		EnvVar: "awsEndpoint",
	})
	c.Flags = append(c.Flags, cli.StringFlag{
		Name:   awsRegion,
		Usage:  "AWS Region",
		Value:  "",
		EnvVar: "awsRegion",
	})
}

// NewS3Client initializes S3Client
func NewS3Client(c *cli.Context) *S3Client {
	return &S3Client{
		accessKeyID:     c.String(awsAccessKeyID),
		secretAccessKey: c.String(awsSecretAccessKey),
		endpoint:        c.String(awsEndpoint),
		region:          c.String(awsRegion),
		inited:          false,
	}
}

// Get get AWS SDK S3 Client
func (s *S3Client) Get() *s3.S3 {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.inited {
		return s.s3
	}
	s.s3 = s.get()
	s.inited = true
	return s.s3
}

func (s *S3Client) get() *s3.S3 {
	log.Info("Initializing S3")
	c := &aws.Config{
		Credentials: credentials.NewStaticCredentials(s.accessKeyID, s.secretAccessKey, ""),
		Endpoint:    aws.String(s.endpoint),
		Region:      aws.String(s.region),
		// DisableSSL:       aws.Bool(true),
		S3ForcePathStyle: aws.Bool(true),
	}
	ss := session.New(c)
	s.s3 = s3.New(ss)
	return s.s3
}
