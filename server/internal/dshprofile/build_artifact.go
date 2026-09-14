package dshprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

type BuildObjectReader interface {
	GetReader(context.Context, string) (io.ReadCloser, error)
}

// VerifyBuildObject is the publication boundary. A successful upload response
// alone does not prove that the exact bytes are available to another replica.
func VerifyBuildObject(ctx context.Context, objects BuildObjectReader, job BuildJob) error {
	if objects == nil || !job.valid() || !job.Artifact.Valid() || job.ArtifactKey != job.objectKey() {
		return errors.New("invalid DSH build artifact publication")
	}
	source, err := objects.GetReader(ctx, job.ArtifactKey)
	if err != nil {
		return errors.New("DSH build object read unavailable")
	}
	defer source.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(source, job.Artifact.ArchiveSize+1))
	if err != nil || size != job.Artifact.ArchiveSize || hex.EncodeToString(digest.Sum(nil)) != job.Artifact.ArchiveSHA256 {
		return errors.New("DSH build stored object integrity mismatch")
	}
	return nil
}
