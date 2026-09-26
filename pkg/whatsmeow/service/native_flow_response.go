package whatsmeow_service

import "encoding/json"

// nativeFlowReply es lo que el teléfono deja en
// interactiveResponseMessage.nativeFlowResponseMessage.paramsJSON al tocar
// un botón nativo:
//
//	quick_reply   → {"id":"<Id>","display_text":"<etiqueta>"}
//	single_select → {"id":"<rowId>","title":"<fila>","description":"<detalle>"}
//
// El backend sólo necesita el `id`; el texto se rellena con display_text o,
// si no viene (listas), con title.
type nativeFlowReply struct {
	ID          string
	Text        string
	Description string
}

// parseNativeFlowReply nunca falla: con JSON vacío o roto devuelve ceros y el
// evento ButtonClick sale igual, con paramsJSON crudo para que el receptor
// decida.
func parseNativeFlowReply(paramsJSON string) nativeFlowReply {
	var out nativeFlowReply
	if paramsJSON == "" {
		return out
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		return out
	}
	str := func(key string) string {
		v, _ := params[key].(string)
		return v
	}
	out.ID = str("id")
	out.Text = str("display_text")
	if out.Text == "" {
		out.Text = str("title")
	}
	out.Description = str("description")
	return out
}
