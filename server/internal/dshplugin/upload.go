package dshplugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// An uploaded package arrives in whatever shape the author had to hand: a zip
// from a GitHub "Download ZIP", the output of `npm pack`, or a plain tarball of
// a directory. The sandbox only unpacks gzipped tar with a single wrapper
// directory, so everything is normalised to that one shape here — once, at
// import — rather than teaching the runtime three formats.
const (
	// MaxUploadBytes bounds an uploaded file before anything is decompressed.
	MaxUploadBytes = 32 << 20
	// wrapperDir is the root npm itself uses, and the one the adapter expects.
	wrapperDir = "package"
)

// NormalizeUpload converts an uploaded archive into the gzipped tar the rest of
// the pipeline reads, re-rooted so the package manifest sits at
// `package/package.json`.
//
// Returns the normalised bytes; validation is the caller's job, and runs on the
// normalised form so an upload is held to exactly the same gates as a package
// fetched from a registry.
func NormalizeUpload(data []byte, filename string) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("the uploaded file is empty")
	}
	if len(data) > MaxUploadBytes {
		return nil, fmt.Errorf("the uploaded file is larger than the %d byte limit", int64(MaxUploadBytes))
	}

	files, err := readUpload(data, filename)
	if err != nil {
		return nil, err
	}
	rooted, err := rerootAtManifest(files)
	if err != nil {
		return nil, err
	}
	return writeTarball(rooted)
}

// readUpload reads either a zip or a gzipped tar into a path -> content map,
// with paths exactly as the archive records them.
func readUpload(data []byte, filename string) (map[string][]byte, error) {
	lower := strings.ToLower(strings.TrimSpace(filename))
	isZip := len(data) > 4 && data[0] == 'P' && data[1] == 'K' &&
		(data[2] == 0x03 || data[2] == 0x05 || data[2] == 0x07)
	isGzip := len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b

	switch {
	case isZip:
		return readZip(data)
	case isGzip:
		return readTarGz(data)
	case strings.HasSuffix(lower, ".zip"):
		return nil, errors.New("the file is named .zip but is not a zip archive")
	case strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar.gz"):
		return nil, errors.New("the file is named as a tarball but is not gzip-compressed")
	}
	return nil, errors.New("the upload must be a .zip or a gzipped .tgz")
}

func readZip(data []byte) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("the zip archive could not be read")
	}
	if len(reader.File) > maxMembers {
		return nil, fmt.Errorf("the archive holds more than %d files", maxMembers)
	}
	files := map[string][]byte{}
	var total int64
	for _, entry := range reader.File {
		if len(entry.Name) > maxMemberNameBytes {
			return nil, errors.New("the archive contains an over-long file name")
		}
		name := path.Clean(strings.ReplaceAll(entry.Name, "\\", "/"))
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("the archive contains an unsafe path: %s", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		// A zip records the unix mode in its external attributes; a symlink
		// there would resolve wherever it points once unpacked.
		if entry.Mode()&stripSymlinkMode != 0 {
			return nil, fmt.Errorf("the archive contains a link, which is not accepted: %s", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maxMemberBytes) {
			return nil, fmt.Errorf("the archive contains a file over the %d byte limit: %s",
				int64(maxMemberBytes), name)
		}
		handle, err := entry.Open()
		if err != nil {
			return nil, errors.New("the zip archive could not be read")
		}
		// Read one byte past the running budget so an inflated member that
		// lies about its size is caught rather than trusted.
		remaining := maxArchiveBytes - total
		body, err := io.ReadAll(io.LimitReader(handle, remaining+1))
		handle.Close()
		if err != nil {
			return nil, errors.New("the zip archive could not be read")
		}
		total += int64(len(body))
		if total > maxArchiveBytes {
			return nil, fmt.Errorf("the archive expands past the %d byte limit", int64(maxArchiveBytes))
		}
		files[name] = body
	}
	if len(files) == 0 {
		return nil, errors.New("the archive holds no files")
	}
	return files, nil
}

// stripSymlinkMode is os.ModeSymlink, spelled out so this file does not import
// os purely for one constant.
const stripSymlinkMode = 1 << 27

func readTarGz(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("the file is not a gzipped tarball")
	}
	defer gz.Close()

	limited := &countingReader{inner: io.LimitReader(gz, maxArchiveBytes+1)}
	reader := tar.NewReader(limited)
	files := map[string][]byte{}
	for count := 0; ; count++ {
		if count >= maxMembers {
			return nil, fmt.Errorf("the archive holds more than %d files", maxMembers)
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("the archive is malformed")
		}
		if limited.read > maxArchiveBytes {
			return nil, fmt.Errorf("the archive expands past the %d byte limit", int64(maxArchiveBytes))
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			return nil, errors.New("the archive carries a global header, which is not accepted")
		}
		if len(header.Name) > maxMemberNameBytes {
			return nil, errors.New("the archive contains an over-long file name")
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("the archive contains an unsafe path: %s", header.Name)
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("the archive contains a link, which is not accepted: %s", header.Name)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > maxMemberBytes {
			return nil, fmt.Errorf("the archive contains a file over the %d byte limit: %s",
				int64(maxMemberBytes), name)
		}
		body, err := io.ReadAll(io.LimitReader(reader, maxMemberBytes+1))
		if err != nil {
			return nil, errors.New("the archive is malformed")
		}
		files[name] = body
	}
	if len(files) == 0 {
		return nil, errors.New("the archive holds no files")
	}
	return files, nil
}

// rerootAtManifest finds the directory holding package.json and re-roots the
// archive there, under npm's own `package/` wrapper.
//
// A "Download ZIP" from GitHub nests everything under `<repo>-<ref>/`, `npm
// pack` uses `package/`, and a hand-zipped folder may have the manifest at the
// top. All three end up identical here.
func rerootAtManifest(files map[string][]byte) (map[string][]byte, error) {
	candidates := make([]string, 0, 2)
	for name := range files {
		if path.Base(name) == "package.json" {
			candidates = append(candidates, path.Dir(name))
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("the archive has no package.json, so it is not a package")
	}
	// Shallowest wins: a nested package.json belongs to a bundled dependency,
	// not to the package being imported.
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := depth(candidates[i]), depth(candidates[j])
		if di != dj {
			return di < dj
		}
		return candidates[i] < candidates[j]
	})
	root := candidates[0]
	if depth(candidates[0]) > 1 {
		return nil, errors.New("the archive nests its package.json too deeply to identify the package root")
	}

	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	out := make(map[string][]byte, len(files))
	for name, body := range files {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			// Anything outside the package directory is not part of the
			// package; GitHub zips carry nothing else, but be explicit.
			continue
		}
		out[wrapperDir+"/"+strings.TrimPrefix(name, prefix)] = body
	}
	if _, ok := out[wrapperDir+"/package.json"]; !ok {
		return nil, errors.New("the archive has no package.json at the package root")
	}
	return out, nil
}

func depth(dir string) int {
	if dir == "." || dir == "" {
		return 0
	}
	return strings.Count(dir, "/") + 1
}

// writeTarball emits a deterministic gzipped tar: entries sorted by name, with
// fixed modes and no timestamps, so the same upload always yields the same
// bytes and therefore the same integrity digest.
func writeTarball(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		body := files[name]
		// No explicit format: USTAR cannot encode a non-ASCII name, a path
		// component over 100 bytes, or a path over 255, and forcing it would
		// reject those packages outright. Letting the writer choose means PAX
		// where needed, which the archive reader accepts — it refuses only a
		// GLOBAL header — and which Python's tarfile reads natively.
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, fmt.Errorf("failed to repack the archive: %w", err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, fmt.Errorf("failed to repack the archive: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("failed to repack the archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("failed to repack the archive: %w", err)
	}
	return buf.Bytes(), nil
}

// InspectUploaded validates already-normalised bytes and reports what they are.
func InspectUploaded(data []byte) (*Resolved, error) {
	resolved, err := inspectArchive(data, Source{Kind: SourceUpload})
	if err != nil {
		return nil, err
	}
	resolved.SizeBytes = int64(len(data))
	return resolved, nil
}
