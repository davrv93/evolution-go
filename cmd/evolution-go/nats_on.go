//go:build !nonats

package main

import (
	config "github.com/evolution-foundation/evolution-go/pkg/config"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	nats_producer "github.com/evolution-foundation/evolution-go/pkg/events/nats"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
)

// Cliente NATS (NATS_URL). Con la etiqueta `nonats` queda fuera del binario
// (ver nats_off.go).
func newNatsProducer(cfg *config.Config, loggerWrapper *logger_wrapper.LoggerManager) (producer_interfaces.Producer, error) {
	if cfg.NatsUrl == "" {
		return nats_producer.NewNatsProducer("", false, nil, loggerWrapper), nil
	}
	logger.LogInfo("NATS enabled")
	return nats_producer.NewNatsProducer(
		cfg.NatsUrl,
		cfg.NatsGlobalEnabled,
		cfg.NatsGlobalEvents,
		loggerWrapper,
	), nil
}
