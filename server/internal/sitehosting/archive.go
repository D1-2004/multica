package sitehosting

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const (
	DefaultMaxArchiveBytes  int64 = 50 << 20
	DefaultMaxExpandedBytes int64 = 200 << 20
	DefaultMaxFileBytes     int64 = 50 << 20
	DefaultMaxFiles               = 2000
)

type Limits struct {
	MaxArchiveBytes  int64
	MaxExpandedBytes int64
	MaxFileBytes     int64
	MaxFiles         int
}

func DefaultLimits() Limits {
	return Limits{
		MaxArchiveBytes:  DefaultMaxArchiveBytes,
		MaxExpandedBytes: DefaultMaxExpandedBytes,
		MaxFileBytes:     DefaultMaxFileBytes,
		MaxFiles:         DefaultMaxFiles,
	}
}

type ArchiveFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	ETag        string `json:"etag,omitempty"`
}

type Manifest struct {
	Entrypoint string                 `json:"entrypoint"`
	Files      map[string]ArchiveFile `json:"files"`
	TotalBytes int64                  `json:"total_bytes"`
}

func ValidateArchive(archivePath string, limits Limits, entrypoint string) (Manifest, error) {
	info, err := os.Stat(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("stat archive: %w", err)
	}
	if info.Size() <= 0 || info.Size() > limits.MaxArchiveBytes {
		return Manifest{}, fmt.Errorf("archive size exceeds %d bytes", limits.MaxArchiveBytes)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open zip archive: %w", err)
	}
	defer reader.Close()

	entrypoint, err = normalizeArchivePath(entrypoint)
	if err != nil {
		return Manifest{}, fmt.Errorf("invalid entrypoint: %w", err)
	}
	manifest := Manifest{Entrypoint: entrypoint, Files: make(map[string]ArchiveFile)}
	canonical := make(map[string]string)
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name, err := normalizeArchivePath(file.Name)
		if err != nil {
			return Manifest{}, fmt.Errorf("unsafe archive path %q: %w", file.Name, err)
		}
		if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("archive entry %q is not a regular file", file.Name)
		}
		if sensitiveArchivePath(name) {
			return Manifest{}, fmt.Errorf("archive entry %q is sensitive", file.Name)
		}
		key := strings.ToLower(norm.NFC.String(name))
		if previous, exists := canonical[key]; exists {
			return Manifest{}, fmt.Errorf("archive paths %q and %q conflict", previous, name)
		}
		canonical[key] = name
		if len(manifest.Files)+1 > limits.MaxFiles {
			return Manifest{}, fmt.Errorf("archive file count exceeds %d", limits.MaxFiles)
		}
		size := int64(file.UncompressedSize64)
		if size < 0 || size > limits.MaxFileBytes {
			return Manifest{}, fmt.Errorf("archive file size exceeds %d bytes for %q", limits.MaxFileBytes, name)
		}
		if size > limits.MaxExpandedBytes-manifest.TotalBytes {
			return Manifest{}, fmt.Errorf("archive expanded size exceeds %d bytes", limits.MaxExpandedBytes)
		}
		if hasPrivateKeyHeader(file) {
			return Manifest{}, fmt.Errorf("archive entry %q contains private key material", file.Name)
		}
		manifest.TotalBytes += size
		manifest.Files[name] = ArchiveFile{Path: name, Size: size, ContentType: contentTypeForPath(name)}
	}
	if _, ok := manifest.Files[entrypoint]; !ok {
		return Manifest{}, fmt.Errorf("archive entrypoint %q is missing", entrypoint)
	}
	return manifest, nil
}

func StreamArchiveFile(archivePath string, target ArchiveFile, destination io.Writer) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != target.Path {
			continue
		}
		source, err := file.Open()
		if err != nil {
			return fmt.Errorf("open archive entry %q: %w", target.Path, err)
		}
		written, copyErr := io.Copy(destination, io.LimitReader(source, target.Size+1))
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("stream archive entry %q: %w", target.Path, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close archive entry %q: %w", target.Path, closeErr)
		}
		if written != target.Size {
			return fmt.Errorf("archive entry %q size changed", target.Path)
		}
		return nil
	}
	return fmt.Errorf("archive entry %q not found", target.Path)
}

func normalizeArchivePath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, `\`) {
		return "", fmt.Errorf("empty or invalid path")
	}
	if strings.Contains(raw, "%") {
		decoded, err := url.PathUnescape(raw)
		if err != nil || decoded != raw {
			return "", fmt.Errorf("encoded path is not allowed")
		}
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) || windowsDriveAbsolutePath(raw) {
		return "", fmt.Errorf("absolute path is not allowed")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != raw {
		return "", fmt.Errorf("path is not canonical")
	}
	return norm.NFC.String(cleaned), nil
}

func windowsDriveAbsolutePath(raw string) bool {
	if len(raw) < 3 || raw[1] != ':' || raw[2] != '/' {
		return false
	}
	return (raw[0] >= 'a' && raw[0] <= 'z') || (raw[0] >= 'A' && raw[0] <= 'Z')
}

func sensitiveArchivePath(name string) bool {
	parts := strings.Split(strings.ToLower(name), "/")
	for _, part := range parts {
		if part == ".git" || part == ".env" || strings.HasPrefix(part, ".env.") {
			return true
		}
	}
	base := parts[len(parts)-1]
	ext := strings.ToLower(path.Ext(base))
	return ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" ||
		base == "id_rsa" || base == "id_dsa" || base == "id_ecdsa" || base == "id_ed25519" ||
		base == "credentials" || strings.HasPrefix(base, "credentials.") ||
		base == ".npmrc" || base == ".pypirc" || base == ".netrc" || base == "service-account.json"
}

func hasPrivateKeyHeader(file *zip.File) bool {
	if file.UncompressedSize64 == 0 {
		return false
	}
	reader, err := file.Open()
	if err != nil {
		return true
	}
	defer reader.Close()
	length := int64(4096)
	if int64(file.UncompressedSize64) < length {
		length = int64(file.UncompressedSize64)
	}
	buffer := make([]byte, length)
	n, err := io.ReadFull(reader, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return true
	}
	buffer = buffer[:n]
	return bytes.Contains(buffer, []byte("-----BEGIN PRIVATE KEY-----")) ||
		bytes.Contains(buffer, []byte("-----BEGIN RSA PRIVATE KEY-----")) ||
		bytes.Contains(buffer, []byte("-----BEGIN EC PRIVATE KEY-----")) ||
		bytes.Contains(buffer, []byte("-----BEGIN OPENSSH PRIVATE KEY-----"))
}

func contentTypeForPath(name string) string {
	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if contentType == "" {
		return "application/octet-stream"
	}
	if strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "charset=") {
		return contentType + "; charset=utf-8"
	}
	return contentType
}
