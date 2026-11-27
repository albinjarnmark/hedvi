// Package legal owns the versions used by the console and its web forms.
// Adoption is an operator/legal decision, never inferred by a migration.
package legal

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/db"
	"regexp"
	"strings"
)

//go:embed manifest.json
var manifestJSON []byte

//go:embed documents/*.json
var documentFS embed.FS

type Document struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	Acknowledgement string `json:"acknowledgement"`
}
type Manifest struct {
	Version   string     `json:"version"`
	Adopted   bool       `json:"adopted"`
	Documents []Document `json:"documents"`
}
type Consent struct {
	Version             string `json:"version"`
	Terms               bool   `json:"terms"`
	AUP                 bool   `json:"aup"`
	PrivacyNotice       bool   `json:"privacy_notice"`
	DPA                 bool   `json:"dpa"`
	SubprocessorsNotice bool   `json:"subprocessors_notice"`
	CookiesNotice       bool   `json:"cookies_notice"`
}

func Current() Manifest {
	var m Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		panic(err)
	}
	return m
}

// ForOrigin changes only presentation links; receipt paths and digests remain canonical.
func ForOrigin(origin string) Manifest {
	m := Current()
	for i := range m.Documents {
		m.Documents[i].URL = strings.TrimRight(origin, "/") + m.Documents[i].URL
	}
	return m
}
func (m Manifest) validate(deployed bool) error {
	if deployed && !m.Adopted {
		return errors.New("hosted terms have not been adopted; account registration is unavailable")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`).MatchString(m.Version) || len(m.Documents) != 6 {
		return errors.New("invalid legal manifest")
	}
	expected := map[string]string{"terms": "agreement", "aup": "agreement", "dpa": "agreement", "privacy": "notice_received", "subprocessors": "notice_received", "cookies": "notice_received"}
	paths := map[string]string{"terms": "/legal/terms", "aup": "/legal/acceptable-use", "privacy": "/legal/privacy", "dpa": "/legal/dpa", "subprocessors": "/legal/subprocessors", "cookies": "/legal/cookies"}
	for _, d := range m.Documents {
		ack, ok := expected[d.ID]
		if !ok || d.Acknowledgement != ack || d.URL != "/legal/versions/"+m.Version+strings.TrimPrefix(paths[d.ID], "/legal") || d.Version == "" || len(d.Version) > 100 {
			return errors.New("invalid legal manifest document")
		}
		delete(expected, d.ID)
		if m.Adopted {
			digest, err := hex.DecodeString(d.SHA256)
			if err != nil || len(digest) != 32 || strings.Contains(d.Version, "unadopted") || strings.Contains(m.Version, "unadopted") {
				return errors.New("invalid adopted legal manifest")
			}
		}
	}
	return nil
}
func Validate(c Consent, deployed bool) error {
	m := Current()
	if err := m.validate(deployed); err != nil {
		return err
	}
	if err := m.validateEmbedded(); err != nil {
		return err
	}
	if c.Version != m.Version || !c.Terms || !c.AUP || !c.DPA || !c.PrivacyNotice || !c.SubprocessorsNotice || !c.CookiesNotice {
		return errors.New("acknowledge the current agreements and privacy notices")
	}
	return nil
}
func Record(ctx context.Context, tx pgx.Tx, user uuid.UUID, c Consent, deployed bool, ip any, ua string, origin string) error {
	if err := Validate(c, deployed); err != nil {
		return err
	}
	runes := []rune(strings.ToValidUTF8(ua, ""))
	if len(runes) > 300 {
		runes = runes[:300]
	}
	ua = string(runes)
	tag, err := tx.Exec(ctx, `INSERT INTO legal_receipts(user_id,manifest_version,documents,ip,user_agent,manifest_adopted,document_origin) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, user, Current().Version, Current().Documents, ip, ua, Current().Adopted, strings.TrimRight(origin, "/"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var matches bool
		err = tx.QueryRow(ctx, `SELECT documents=$3::jsonb AND manifest_adopted=$4 FROM legal_receipts WHERE user_id=$1 AND manifest_version=$2`, user, Current().Version, Current().Documents, Current().Adopted).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return errors.New("legal document contents changed without a new manifest version")
		}
	}
	return nil
}
func HasCurrent(ctx context.Context, pool *pgxpool.Pool, user uuid.UUID, deployed bool) (bool, error) {
	if Current().validate(deployed) != nil || Current().validateEmbedded() != nil {
		return false, nil
	}
	var found bool
	err := db.InUser(ctx, pool, user, nil, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legal_receipts WHERE user_id=$1 AND manifest_version=$2 AND documents=$3::jsonb AND manifest_adopted=$4)`, user, Current().Version, Current().Documents, Current().Adopted).Scan(&found)
	})
	return found, err
}

func (m Manifest) validateEmbedded() error {
	for _, d := range m.Documents {
		name := d.ID
		if name == "aup" {
			name = "acceptable-use"
		}
		raw, err := documentFS.ReadFile("documents/" + name + ".json")
		if err != nil {
			return errors.New("legal content missing from this build")
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != d.SHA256 {
			return errors.New("legal content differs from its receipt digest")
		}
		var doc struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &doc) != nil || doc.Version != d.Version {
			return errors.New("legal content version mismatch")
		}
	}
	return nil
}
