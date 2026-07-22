package main

import "testing"

func TestS3KeyPrefix(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *S3Config
		serverSlug string
		database   string
		want       string
	}{
		{
			name:       "no prefix configured",
			cfg:        &S3Config{Bucket: "b"},
			serverSlug: "prod-pg",
			database:   "app",
			want:       "prod-pg/app",
		},
		{
			name:       "prefix configured",
			cfg:        &S3Config{Bucket: "b", Prefix: "homelab"},
			serverSlug: "prod-pg",
			database:   "app",
			want:       "homelab/prod-pg/app",
		},
		{
			name:       "prefix with leading/trailing slashes is trimmed",
			cfg:        &S3Config{Bucket: "b", Prefix: "/homelab/"},
			serverSlug: "prod-pg",
			database:   "app",
			want:       "homelab/prod-pg/app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s3KeyPrefix(tt.cfg, tt.serverSlug, tt.database)
			if got != tt.want {
				t.Errorf("s3KeyPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}
