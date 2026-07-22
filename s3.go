package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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

func selectKeysToDelete(keys []string, keepCount int) []string {
	if keepCount <= 0 || len(keys) <= keepCount {
		return nil
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return sorted[:len(sorted)-keepCount]
}

func uploadToS3(ctx context.Context, client *s3.Client, cfg *S3Config, serverSlug, database, localFile string) error {
	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("open local file: %w", err)
	}
	defer f.Close()

	key := s3KeyPrefix(cfg, serverSlug, database) + "/" + filepath.Base(localFile)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &cfg.Bucket,
		Key:    &key,
		Body:   f,
	})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}

	log.Printf("backup: uploaded s3://%s/%s", cfg.Bucket, key)
	return nil
}

func pruneS3Backups(ctx context.Context, client *s3.Client, cfg *S3Config, serverSlug, database string, keepCount int) error {
	if keepCount <= 0 {
		return nil
	}

	prefix := s3KeyPrefix(cfg, serverSlug, database) + "/"
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: &cfg.Bucket,
		Prefix: &prefix,
	})
	if err != nil {
		return fmt.Errorf("list objects %s: %w", prefix, err)
	}

	keys := make([]string, 0, len(out.Contents))
	for _, obj := range out.Contents {
		keys = append(keys, *obj.Key)
	}

	for _, key := range selectKeysToDelete(keys, keepCount) {
		k := key
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &cfg.Bucket,
			Key:    &k,
		}); err != nil {
			log.Printf("backup: prune s3://%s/%s: %v", cfg.Bucket, key, err)
		} else {
			log.Printf("backup: pruned s3://%s/%s", cfg.Bucket, key)
		}
	}
	return nil
}
