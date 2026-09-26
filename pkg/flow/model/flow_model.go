package flow_model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Estados de FlowDef y FlowRun. Palabras propias: el color lo pone la UI.
const (
	EstadoBorrador  = "borrador"
	EstadoActivo    = "activo"
	EstadoPausado   = "pausado"
	EstadoArchivado = "archivado"

	RunEnCurso    = "en_curso"
	RunCompletado = "completado"
	RunAbandonado = "abandonado"
	RunDerivado   = "derivado"
)

// Audiencias de un flujo por defecto (orquestador): a quién atiende cuando
// ningún run ni entrada reclama el mensaje. La decide el pod (callback «rol»).
const (
	AudienciaCliente  = "cliente"
	AudienciaGerencia = "gerencia"
)

// FlowDef es un flujo conversacional de una instancia: entrada que lo dispara
// y pasos en JSON (los valida flow_service; aquí solo se guardan).
type FlowDef struct {
	Id          string          `json:"id" gorm:"type:uuid;primaryKey"`
	InstanceID  string          `json:"instanceId" gorm:"type:uuid;index"`
	Nombre      string          `json:"nombre"`
	Descripcion string          `json:"descripcion"`
	Estado      string          `json:"estado" gorm:"index"`
	Entrada     string          `json:"entrada" gorm:"index"`
	Steps       json.RawMessage `json:"steps" gorm:"type:jsonb"`
	// Orquestador (flow_orquestador.go). Un flujo por defecto ACTIVO atiende
	// a su audiencia (cliente | gerencia) cuando ningún run ni entrada reclama
	// el mensaje, y el menú clásico del pod para esa audiencia calla. Vacío y
	// false = flujo normal, que solo dispara su entrada.
	Audiencia  string    `json:"audiencia" gorm:"type:varchar(20);not null;default:''"`
	PorDefecto bool      `json:"porDefecto" gorm:"not null;default:false"`
	CreatedAt  time.Time `json:"createdAt" gorm:"autoCreateTime"`
	UpdatedAt  time.Time `json:"updatedAt" gorm:"autoUpdateTime"`
}

func (m *FlowDef) BeforeCreate(tx *gorm.DB) (err error) {
	m.Id = uuid.New().String()
	return
}

// FlowRun es la conversación en curso de un remitente dentro de un flujo:
// paso actual + respuestas ya dadas (contexto). El sweeper marca abandonado
// lo que supere el TTL sin moverse.
type FlowRun struct {
	Id         string          `json:"id" gorm:"type:uuid;primaryKey"`
	FlowID     string          `json:"flowId" gorm:"type:uuid;index"`
	InstanceID string          `json:"instanceId" gorm:"type:uuid;index"`
	Remitente  string          `json:"remitente" gorm:"index"`
	StepActual string          `json:"stepActual"`
	Contexto   json.RawMessage `json:"contexto" gorm:"type:jsonb"`
	Estado     string          `json:"estado" gorm:"index"`
	CreatedAt  time.Time       `json:"createdAt" gorm:"autoCreateTime"`
	UpdatedAt  time.Time       `json:"updatedAt" gorm:"autoUpdateTime"`
}

func (m *FlowRun) BeforeCreate(tx *gorm.DB) (err error) {
	m.Id = uuid.New().String()
	return
}
