package main

import (
	"context"
	"fmt"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newS3Client(ctx context.Context, cfg *S3Config) (*s3.Client, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			endpoint := cfg.Endpoint
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})
	return client, nil
}

func s3KeyPrefix(cfg *S3Config, serverSlug, database string) string {
	parts := make([]string, 0, 3)
	if trimmed := strings.Trim(cfg.Prefix, "/"); trimmed != "" {
		parts = append(parts, trimmed)
	}
	parts = append(parts, serverSlug, database)
	return strings.Join(parts, "/")
}
