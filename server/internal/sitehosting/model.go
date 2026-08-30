package sitehosting

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrUnavailable             = errors.New("static site hosting is unavailable")
	ErrSiteNotFound            = errors.New("static site not found")
	ErrSiteForbidden           = errors.New("static site operation is forbidden")
	ErrUploadCapabilityInvalid = errors.New("upload capability is invalid or expired")
)

type Upload struct {
	ID             string
	SiteID         string
	PublicSiteID   string
	RevisionID     string
	OwnerUserID    string
	TokenHash      []byte
	ExpectedSHA256 string
	ExpectedLength int64
	Entrypoint     string
	SPAFallback    bool
	ExpiresAt      time.Time
	UsedAt         *time.Time
}

type PrepareRecord struct {
	ExistingSiteID string
	Upload         Upload
}

type Activation struct {
	UploadID     string
	SiteID       string
	PublicSiteID string
	RevisionID   string
	Manifest     Manifest
	ArchiveSHA   string
	SPAFallback  bool
	ActivatedAt  time.Time
}

type SiteStatus struct {
	SiteID           string     `json:"site_id"`
	PublicSiteID     string     `json:"public_site_id"`
	OwnerUserID      string     `json:"-"`
	Status           string     `json:"status"`
	ActiveRevisionID *string    `json:"active_revision_id,omitempty"`
	LatestRevisionID string     `json:"latest_revision_id"`
	LatestStatus     string     `json:"latest_status"`
	LatestError      string     `json:"latest_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SiteURL          string     `json:"site_url"`
}

type ResolvedSite struct {
	SiteID       string
	PublicSiteID string
	RevisionID   string
	Manifest     Manifest
	SPAFallback  bool
}

type Store interface {
	Prepare(context.Context, PrepareRecord) (Upload, error)
	ClaimUpload(context.Context, string, []byte, time.Time) (Upload, error)
	ActivateRevision(context.Context, Activation) error
	FailRevision(context.Context, string, string) error
	GetStatus(context.Context, string, string) (SiteStatus, error)
	ListSites(context.Context, string) ([]SiteStatus, error)
	DeleteSite(context.Context, string, string) error
	ResolvePublic(context.Context, string) (ResolvedSite, error)
}

type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
