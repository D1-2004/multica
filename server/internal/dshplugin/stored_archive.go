package dshplugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/storage"
)

var ErrArchiveIdentity = errors.New("plugin source does not match its recorded package")
var archiveIntegrity = regexp.MustCompile(`^sha256-[a-f0-9]{64}$`)

func StoredArchiveKey(workspace, name, integrity string) (string, error) {
	if workspace == "" || !archiveIntegrity.MatchString(integrity) {
		return "", ErrArchiveIdentity
	}
	return fmt.Sprintf("dsh-plugins/%s/%s/%s.tgz", workspace, strings.ReplaceAll(name, "/", "__"), strings.TrimPrefix(integrity, "sha256-")), nil
}

// EnsureStoredArchive recovers an interrupted import from its pinned source.
// Both the package identity and exact archive bytes must match the original
// validation; a changed upstream package is never silently accepted.
func EnsureStoredArchive(ctx context.Context, objects storage.Storage, resolver *Resolver, key, name, version, integrity, source string) error {
	if objects == nil || resolver == nil || key == "" {
		return errors.New("plugin archive storage unavailable")
	}
	if reader, err := objects.GetReader(ctx, key); err == nil {
		return reader.Close()
	}
	parsed, err := ParseSource(source)
	if err != nil || parsed.Kind == SourceUpload || parsed.Kind == SourceFile {
		return ErrArchiveIdentity
	}
	if parsed.Kind == SourceNPM {
		if parsed.Name != name {
			return ErrArchiveIdentity
		}
		parsed.Version = version
		parsed.Spec = "npm:" + name + "@" + version
	}
	resolved, err := resolver.Resolve(ctx, parsed)
	if err != nil {
		return errors.New("plugin source archive download failed")
	}
	if resolved.PackageName != name || resolved.Version != version || resolved.Integrity != integrity {
		return ErrArchiveIdentity
	}
	_, err = objects.Upload(ctx, key, resolved.Archive, "application/gzip", name+".tgz")
	return err
}
