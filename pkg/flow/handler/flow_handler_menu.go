package flow_handler

// Menús por flujo: audiencia y «por defecto» de un flujo (orquestador).

import (
	"context"
	"errors"
	"strings"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
)

// aplicarAudiencia pone audiencia y porDefecto SOLO si vinieron en el
// cuerpo. Un flujo por defecto necesita audiencia: es a quién atiende.
func aplicarAudiencia(def *flow_model.FlowDef, in definicionEntrada) error {
	if in.Audiencia != nil {
		a := strings.TrimSpace(*in.Audiencia)
		switch a {
		case "", flow_model.AudienciaCliente, flow_model.AudienciaGerencia:
			def.Audiencia = a
		default:
			return errors.New("audiencia desconocida: usa cliente o gerencia")
		}
	}
	if in.PorDefecto != nil {
		def.PorDefecto = *in.PorDefecto
	}
	if def.PorDefecto && def.Audiencia == "" {
		return errors.New("un flujo por defecto necesita audiencia (cliente o gerencia)")
	}
	return nil
}

// pausarOtrosPorDefecto: al activar el menú de una audiencia, el que hubiera
// activo antes se pausa. Dos menús por defecto para la misma audiencia no
// tienen sentido y el orquestador solo usaría uno.
func (h *flowHandler) pausarOtrosPorDefecto(c context.Context, def *flow_model.FlowDef) {
	defs, err := h.repo.DefsPorInstancia(c, def.InstanceID)
	if err != nil {
		return
	}
	for i := range defs {
		o := &defs[i]
		if o.Id == def.Id || !o.PorDefecto || o.Audiencia != def.Audiencia || o.Estado != flow_model.EstadoActivo {
			continue
		}
		o.Estado = flow_model.EstadoPausado
		_ = h.repo.ActualizarDef(c, o)
	}
}
