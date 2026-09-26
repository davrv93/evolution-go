package flow_handler

import (
	"encoding/json"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	flow_service "github.com/evolution-foundation/evolution-go/pkg/flow/service"
)

func pasosJSON(def flow_service.Definicion) json.RawMessage {
	b, _ := json.Marshal(def)
	if len(b) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}

func defSteps(rec *flow_model.FlowDef, def *flow_service.Definicion) error {
	return json.Unmarshal(rec.Steps, def)
}
