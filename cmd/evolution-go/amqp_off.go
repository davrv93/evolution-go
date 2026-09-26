//go:build noamqp

package main

import (
	"errors"
	"log"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
)

// Build sin RabbitMQ (`-tags noamqp`, la de la imagen de producción, donde
// no hay AMQP_URL): amqp091-go queda fuera del binario. Si AMQP_URL viene
// puesto, el arranque falla en vez de perder eventos en silencio.
type rabbitConn = *struct{}

func dialRabbitMQ(cfg *config.Config) (rabbitConn, func()) {
	if cfg.AmqpUrl != "" {
		log.Fatal("AMQP_URL está definido pero el binario se compiló con -tags noamqp: recompile sin esa etiqueta (GO_TAGS) o quite AMQP_URL")
	}
	logger.LogInfo("RabbitMQ URL not configured, skipping RabbitMQ connection")
	return nil, func() {}
}

func newRabbitMQProducer(rabbitConn, *config.Config, *logger_wrapper.LoggerManager) producer_interfaces.Producer {
	return disabledRabbitMQ{}
}

// disabledRabbitMQ reproduce lo que hace el productor real sin AMQP_URL:
// Produce devuelve error (y quien llama registra el fallo y no sigue con ese
// evento, igual que antes) y CreateGlobalQueues no hace nada.
type disabledRabbitMQ struct{}

func (disabledRabbitMQ) Produce(string, []byte, string, string) error {
	return errors.New("RabbitMQ connection string is empty - check AMQP_URL configuration")
}

func (disabledRabbitMQ) CreateGlobalQueues() error { return nil }
