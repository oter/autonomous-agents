package run

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Bucket is the S3-compatible object storage every Journal lands in (SPEC
// §10, ADR-0005). The control plane only mints presigned URLs for it: it
// never reads or writes an object itself, and its credential never leaves
// it. URL is the bucket's own base URL, path-style: <endpoint>/<bucket>.
type Bucket struct {
	URL       url.URL
	Region    string // "auto" on R2
	AccessKey string
	SecretKey string
}

// JournalURLExpiry bounds a presigned URL. They are minted when Teardown
// starts (ADR-0005), so one only has to outlive an upload, not the Run.
const JournalURLExpiry = 15 * time.Minute

// Presign returns a URL for method on key that carries its own SigV4
// signature in the query string, valid for expires from at. Stdlib rather
// than an AWS SDK: this is the only S3 call the control plane makes.
func (b Bucket) Presign(method, key string, at time.Time, expires time.Duration) string {
	u := b.URL
	u.Path = path.Join("/", u.Path, key)
	date := at.UTC().Format("20060102T150405Z")
	scope := date[:8] + "/" + b.Region + "/s3/aws4_request"
	q := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {b.AccessKey + "/" + scope},
		"X-Amz-Date":          {date},
		"X-Amz-Expires":       {strconv.Itoa(int(expires.Seconds()))},
		"X-Amz-SignedHeaders": {"host"},
	}
	canonical := strings.Join([]string{method, u.EscapedPath(), q.Encode(), "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", date, scope, hex.EncodeToString(sum[:])}, "\n")
	k := []byte("AWS4" + b.SecretKey)
	for _, s := range []string{date[:8], b.Region, "s3", "aws4_request"} {
		k = hmacSHA256(k, s)
	}
	q.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(k, toSign)))
	u.RawQuery = q.Encode()
	return u.String()
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}
