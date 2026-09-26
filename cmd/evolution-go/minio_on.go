//go:build !nominio

package main

import (
	config "github.com/evolution-foundation/evolution-go/pkg/config"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	minio_storage "github.com/evolution-foundation/evolution-go/pkg/storage/minio"
)

// Almacén de medios en MinIO/S3 (MINIO_ENABLED=true). Con la etiqueta
// `nominio` el cliente queda fuera del binario (ver minio_off.go).
func newMinioMediaStorage(cfg *config.Config) (storage_interfaces.MediaStorage, error) {
	return minio_storage.NewMinioMediaStorage(
		cfg.MinioEndpoint,
		cfg.MinioAccessKey,
		cfg.MinioSecretKey,
		cfg.MinioBucket,
		cfg.MinioRegion,
		cfg.MinioUseSSL,
	)
}
