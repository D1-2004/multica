package sitehosting

import (
	"context"
	"fmt"
	"io"
)

type streamingStorage interface {
	Upload(context.Context, string, []byte, string, string) (string, error)
	UploadStream(context.Context, string, io.Reader, int64, string, string) (string, error)
	GetReader(context.Context, string) (io.ReadCloser, error)
	DeleteObject(context.Context, string) error
}

type storageObjectStore struct {
	storage streamingStorage
}

func NewStorageObjectStore(value any) ObjectStore {
	storage, ok := value.(streamingStorage)
	if !ok || storage == nil {
		return nil
	}
	return &storageObjectStore{storage: storage}
}

func (s *storageObjectStore) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	if size < 0 {
		return fmt.Errorf("static site object %q has invalid content length", key)
	}
	if size == 0 {
		_, err := s.storage.Upload(ctx, key, []byte{}, contentType, "")
		return err
	}
	_, err := s.storage.UploadStream(ctx, key, reader, size, contentType, "")
	return err
}

func (s *storageObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.storage.GetReader(ctx, key)
}

func (s *storageObjectStore) Delete(ctx context.Context, key string) error {
	return s.storage.DeleteObject(ctx, key)
}
