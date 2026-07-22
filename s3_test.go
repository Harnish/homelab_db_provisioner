package main

import "testing"

func TestSelectKeysToDelete(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		keepCount int
		want      []string
	}{
		{
			name:      "keepCount zero keeps everything",
			keys:      []string{"a/db_2026-01-01.sql.gz", "a/db_2026-01-02.sql.gz"},
			keepCount: 0,
			want:      nil,
		},
		{
			name:      "fewer keys than keepCount deletes nothing",
			keys:      []string{"a/db_2026-01-01.sql.gz"},
			keepCount: 5,
			want:      nil,
		},
		{
			name: "deletes oldest beyond keepCount",
			keys: []string{
				"a/db_2026-01-03.sql.gz",
				"a/db_2026-01-01.sql.gz",
				"a/db_2026-01-02.sql.gz",
			},
			keepCount: 1,
			want:      []string{"a/db_2026-01-01.sql.gz", "a/db_2026-01-02.sql.gz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectKeysToDelete(tt.keys, tt.keepCount)
			if len(got) != len(tt.want) {
				t.Fatalf("selectKeysToDelete() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("selectKeysToDelete()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

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
