// Package blob stores email payloads. Payloads are always encrypted by the
// application (AES-256-GCM bound to the object key) and additionally with
// SSE-KMS when stored in S3.
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	"hedvi.dev/api/internal/secure"
)

var ErrNotFound = errors.New("blob not found")

type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// List calls fn for each key under prefix last modified before olderThan.
	List(ctx context.Context, prefix string, olderThan time.Time, fn func(key string) error) error
}

const PayloadPrefix = "payloads/"

// PayloadKey places payloads under the organization so storage access can be
// audited and restricted per tenant.
func PayloadKey(orgID, emailID uuid.UUID) string {
	return PayloadPrefix + orgID.String() + "/" + emailID.String()
}

// OrgOfKey returns the organization id embedded in a payload key.
func OrgOfKey(key string) (uuid.UUID, error) {
	suffix, ok := strings.CutPrefix(key, PayloadPrefix)
	if !ok {
		return uuid.Nil, errors.New("not a payload key")
	}
	parts := strings.Split(suffix, "/")
	if len(parts) != 2 {
		return uuid.Nil, fmt.Errorf("not a payload key: %q", key)
	}
	org, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || key != PayloadKey(org, id) {
		return uuid.Nil, errors.New("invalid payload key")
	}
	return org, nil
}

var validKey = regexp.MustCompile(`^[a-z0-9][a-z0-9/-]{0,200}$`)

func checkKey(key string) error {
	if !validKey.MatchString(key) || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return fmt.Errorf("invalid blob key %q", key)
	}
	return nil
}

// Encrypted wraps a store with application-level authenticated encryption.
type Encrypted struct {
	Inner  Store
	Sealer *secure.Sealer
}

func (e Encrypted) Put(ctx context.Context, key string, data []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	sealed, err := e.Sealer.Seal(data, []byte(key))
	if err != nil {
		return err
	}
	return e.Inner.Put(ctx, key, sealed)
}

func (e Encrypted) Get(ctx context.Context, key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	sealed, err := e.Inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return e.Sealer.Open(sealed, []byte(key))
}

func (e Encrypted) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	return e.Inner.Delete(ctx, key)
}

func (e Encrypted) List(ctx context.Context, prefix string, olderThan time.Time, fn func(string) error) error {
	return e.Inner.List(ctx, prefix, olderThan, fn)
}

// FS is the local development store.
type FS struct{ Root string }

func (f FS) path(key string) string { return filepath.Join(f.Root, filepath.FromSlash(key)) }

func (f FS) Put(_ context.Context, key string, data []byte) error {
	p := f.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp-" + uuid.NewString()
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (f FS) Get(_ context.Context, key string) ([]byte, error) {
	fh, err := os.Open(f.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return readStoredPayload(fh, nil, maxStoredBytes)
}

func (f FS) Delete(_ context.Context, key string) error {
	err := os.Remove(f.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (f FS) List(ctx context.Context, prefix string, olderThan time.Time, fn func(string) error) error {
	root := f.path(prefix)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || strings.Contains(d.Name(), ".tmp-") {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(olderThan) {
			return nil
		}
		rel, err := filepath.Rel(f.Root, p)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel))
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// S3 stores objects with SSE-KMS. The bucket must block public access; see
// infra/terraform/modules/payloads.
type S3 struct {
	Client   *s3.Client
	Bucket   string
	KMSKeyID string
}

func (s S3) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(s.Bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(data),
		ContentLength:        aws.Int64(int64(len(data))),
		ContentType:          aws.String("application/octet-stream"),
		ServerSideEncryption: s3types.ServerSideEncryptionAwsKms,
		SSEKMSKeyId:          aws.String(s.KMSKeyID),
		BucketKeyEnabled:     aws.Bool(true),
	})
	return err
}

func (s S3) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer out.Body.Close()
	return readStoredPayload(out.Body, out.ContentLength, maxStoredBytes)
}

// Historical canonical JSON can expand each byte of a 15 MiB API body to six
// bytes, plus encryption overhead. Check the actual stream even when a server
// omits or understates Content-Length.
const maxStoredBytes = 96 << 20

func readStoredPayload(r io.Reader, length *int64, limit int64) ([]byte, error) {
	if length != nil && *length > limit {
		return nil, errors.New("stored object exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("stored object exceeds size limit")
	}
	return data, err
}

func (s S3) Delete(ctx context.Context, key string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	return err
}

func (s S3) List(ctx context.Context, prefix string, olderThan time.Time, fn func(string) error) error {
	p := s3.NewListObjectsV2Paginator(s.Client, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if obj.LastModified != nil && obj.LastModified.Before(olderThan) {
				if err := fn(aws.ToString(obj.Key)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
