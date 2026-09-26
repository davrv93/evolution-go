//go:build !noamqp

package main

import (
	"time"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	rabbitmq_producer "github.com/evolution-foundation/evolution-go/pkg/events/rabbitmq"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Cliente RabbitMQ (AMQP_URL). Con la etiqueta `noamqp` queda fuera del
// binario (ver amqp_off.go).
type rabbitConn = *amqp.Connection

// dialRabbitMQ abre la conexión inicial si hay AMQP_URL. Devuelve también la
// función que la cierra al apagar (no hace nada si no hubo conexión).
func dialRabbitMQ(cfg *config.Config) (rabbitConn, func()) {
	if cfg.AmqpUrl == "" {
		logger.LogInfo("RabbitMQ URL not configured, skipping RabbitMQ connection")
		return nil, func() {}
	}

	logger.LogInfo("Attempting to connect to RabbitMQ...")

	// Create connection with heartbeat to prevent timeouts
	amqpConfig := amqp.Config{
		Heartbeat: 30 * time.Second, // Send heartbeat every 30 seconds
		Locale:    "en_US",
	}

	conn, err := amqp.DialConfig(cfg.AmqpUrl, amqpConfig)
	if err != nil {
		logger.LogError("Failed to connect to RabbitMQ, err: %v", err)
		logger.LogInfo("RabbitMQ producer will be created with reconnection capability")
		return nil, func() {}
	}

	logger.LogInfo("Successfully connected to RabbitMQ with heartbeat enabled")
	return conn, func() {
		if err := conn.Close(); err != nil {
			logger.LogError("Failed to close RabbitMQ connection, err: %v", err)
		}
	}
}

func newRabbitMQProducer(conn rabbitConn, cfg *config.Config, loggerWrapper *logger_wrapper.LoggerManager) producer_interfaces.Producer {
	if conn != nil {
		logger.LogInfo("RabbitMQ enabled")
	}
	// Even if initial connection failed, pass the URL so reconnection can work
	return rabbitmq_producer.NewRabbitMQProducer(
		conn,
		cfg.AmqpGlobalEnabled,
		cfg.AmqpGlobalEvents,
		cfg.AmqpSpecificEvents,
		cfg.AmqpUrl,
		loggerWrapper,
	)
}
