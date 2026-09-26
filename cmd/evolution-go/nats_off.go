//go:build nonats

package main

import (
	"errors"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
)

// Build sin NATS (`-tags nonats`, la de la imagen de producción, donde no hay
// NATS_URL): nats.go, nkeys y klauspost/compress quedan fuera del binario.
// Si NATS_URL viene puesto, el arranque falla en vez de perder eventos.
func newNatsProducer(cfg *config.Config, _ *logger_wrapper.LoggerManager) (producer_interfaces.Producer, error) {
	if cfg.NatsUrl != "" {
		return nil, errors.New("NATS_URL está definido pero el binario se compiló con -tags nonats: recompile sin esa etiqueta (GO_TAGS) o quite NATS_URL")
	}
	return disabledNats{}, nil
}

// disabledNats reproduce el productor real sin conexión: Produce no publica
// y devuelve nil (el real sólo registra «NATS connection is nil»).
type disabledNats struct{}

func (disabledNats) Produce(string, []byte, string, string) error { return nil }

func (disabledNats) CreateGlobalQueues() error { return nil }
