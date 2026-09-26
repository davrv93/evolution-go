//go:build nominio

package main

import (
	"errors"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
)

// Build sin MinIO (`-tags nominio`, la de la imagen de producción, donde
// MINIO_ENABLED=false). minio-go arrastra github.com/goccy/go-json, cuyo
// init() reserva en Linux ~2 MB de cachés de tipos en el heap aunque nadie
// serialice nada: era la mayor partida del heap vivo en reposo. Si alguien
// enciende MINIO_ENABLED con este binario, el arranque falla en vez de
// guardar medios en ninguna parte.
func newMinioMediaStorage(*config.Config) (storage_interfaces.MediaStorage, error) {
	return nil, errors.New("MINIO_ENABLED=true pero el binario se compiló con -tags nominio: recompile sin esa etiqueta (GO_TAGS) o ponga MINIO_ENABLED=false")
}
