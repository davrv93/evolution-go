package flow_repository

import (
	"context"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	"gorm.io/gorm"
)

type FlowRepository interface {
	DefsPorInstancia(ctx context.Context, instanceID string) ([]flow_model.FlowDef, error)
	DefPorID(ctx context.Context, id, instanceID string) (*flow_model.FlowDef, error)
	CrearDef(ctx context.Context, def *flow_model.FlowDef) error
	ActualizarDef(ctx context.Context, def *flow_model.FlowDef) error
	EliminarDef(ctx context.Context, id, instanceID string) error
	RunActivo(ctx context.Context, instanceID, remitente string) (*flow_model.FlowRun, error)
	CrearRun(ctx context.Context, run *flow_model.FlowRun) error
	GuardarRun(ctx context.Context, run *flow_model.FlowRun) error
	MarcarRun(ctx context.Context, id, estado string) error
	RunsPorFlow(ctx context.Context, flowID, estado string, limite int) ([]flow_model.FlowRun, error)
}

type flowRepository struct {
	db *gorm.DB
}

func NewFlowRepository(db *gorm.DB) FlowRepository {
	return &flowRepository{db: db}
}

func (r *flowRepository) DefsPorInstancia(ctx context.Context, instanceID string) ([]flow_model.FlowDef, error) {
	var defs []flow_model.FlowDef
	err := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Order("updated_at DESC").
		Find(&defs).Error
	return defs, err
}

func (r *flowRepository) DefPorID(ctx context.Context, id, instanceID string) (*flow_model.FlowDef, error) {
	var def flow_model.FlowDef
	err := r.db.WithContext(ctx).
		Where("id = ? AND instance_id = ?", id, instanceID).
		First(&def).Error
	if err != nil {
		return nil, err
	}
	return &def, nil
}

func (r *flowRepository) CrearDef(ctx context.Context, def *flow_model.FlowDef) error {
	return r.db.WithContext(ctx).Create(def).Error
}

func (r *flowRepository) ActualizarDef(ctx context.Context, def *flow_model.FlowDef) error {
	return r.db.WithContext(ctx).
		Where("id = ? AND instance_id = ?", def.Id, def.InstanceID).
		Save(def).Error
}

func (r *flowRepository) EliminarDef(ctx context.Context, id, instanceID string) error {
	return r.db.WithContext(ctx).
		Where("id = ? AND instance_id = ?", id, instanceID).
		Delete(&flow_model.FlowDef{}).Error
}

func (r *flowRepository) RunActivo(ctx context.Context, instanceID, remitente string) (*flow_model.FlowRun, error) {
	var run flow_model.FlowRun
	err := r.db.WithContext(ctx).
		Where("instance_id = ? AND remitente = ? AND estado = ?", instanceID, remitente, flow_model.RunEnCurso).
		Order("updated_at DESC").
		First(&run).Error
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *flowRepository) CrearRun(ctx context.Context, run *flow_model.FlowRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *flowRepository) GuardarRun(ctx context.Context, run *flow_model.FlowRun) error {
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *flowRepository) MarcarRun(ctx context.Context, id, estado string) error {
	return r.db.WithContext(ctx).
		Model(&flow_model.FlowRun{}).
		Where("id = ?", id).
		Update("estado", estado).Error
}

func (r *flowRepository) RunsPorFlow(ctx context.Context, flowID, estado string, limite int) ([]flow_model.FlowRun, error) {
	var runs []flow_model.FlowRun
	q := r.db.WithContext(ctx).Where("flow_id = ?", flowID)
	if estado != "" {
		q = q.Where("estado = ?", estado)
	}
	if limite <= 0 || limite > 200 {
		limite = 50
	}
	err := q.Order("updated_at DESC").Limit(limite).Find(&runs).Error
	return runs, err
}
