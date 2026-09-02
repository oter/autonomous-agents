package run_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/run"
)

func mustURL(t *testing.T, s string) url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}

// The worked example from "Authenticating Requests: Using Query Parameters
// (AWS Signature Version 4)" in the S3 API reference: a known-good
// signature from an independent source, not one this code computed.
func TestPresignMatchesAWSWorkedExample(t *testing.T) {
	b := run.Bucket{
		URL:       mustURL(t, "https://examplebucket.s3.amazonaws.com"),
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	got := b.Presign("GET", "test.txt", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC), 24*time.Hour)
	want := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404" +
		"&X-Amz-SignedHeaders=host"
	if got != want {
		t.Errorf("presigned URL\n got %s\nwant %s", got, want)
	}
}

// SPEC §10 layout under a path-style bucket URL, as R2 and MinIO take it.
func TestPresignPathStyleLayout(t *testing.T) {
	b := run.Bucket{URL: mustURL(t, "http://localhost:9000/agentruns"), Region: "auto", AccessKey: "minio", SecretKey: "minio123"}
	got := b.Presign("PUT", "hello/20260902-140000-hello-1a2b/meta.json", time.Now(), run.JournalURLExpiry)
	want := "http://localhost:9000/agentruns/hello/20260902-140000-hello-1a2b/meta.json?X-Amz-Algorithm="
	if !strings.HasPrefix(got, want) {
		t.Errorf("presigned URL = %s, want prefix %s", got, want)
	}
}
