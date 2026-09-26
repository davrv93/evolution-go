package whatsmeow_service

import (
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestParseNativeFlowReply_QuickReply(t *testing.T) {
	got := parseNativeFlowReply(`{"id":"wa2_si","display_text":"Sí, confirmo"}`)
	if got.ID != "wa2_si" || got.Text != "Sí, confirmo" || got.Description != "" {
		t.Fatalf("unexpected reply: %+v", got)
	}
}

func TestParseNativeFlowReply_SingleSelectFallsBackToTitle(t *testing.T) {
	got := parseNativeFlowReply(`{"id":"wa2_row_1","title":"Plan básico","description":"S/ 29,90"}`)
	if got.ID != "wa2_row_1" {
		t.Fatalf("id = %q, want wa2_row_1", got.ID)
	}
	if got.Text != "Plan básico" {
		t.Fatalf("text = %q, want the row title when display_text is absent", got.Text)
	}
	if got.Description != "S/ 29,90" {
		t.Fatalf("description = %q, want S/ 29,90", got.Description)
	}
}

func TestParseNativeFlowReply_ToleratesGarbage(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"id":42}`, `[]`} {
		got := parseNativeFlowReply(raw)
		if got.ID != "" || got.Text != "" || got.Description != "" {
			t.Fatalf("parseNativeFlowReply(%q) = %+v, want zero value", raw, got)
		}
	}
}

// El manejador de eventos descarta lo que GetMessageType clasifica como
// "ignore" o "unknown_protocol_*". La respuesta a un botón nativo no puede
// caer ahí, o el backend nunca vería el id.
func TestInteractiveResponseIsNotDiscardedByMessageType(t *testing.T) {
	msg := &waE2E.Message{
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			Body: &waE2E.InteractiveResponseMessage_Body{Text: stringPtr("Sí, confirmo")},
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					Name:       stringPtr("quick_reply"),
					ParamsJSON: stringPtr(`{"id":"wa2_si","display_text":"Sí, confirmo"}`),
					Version:    int32Ptr(1),
				},
			},
		},
	}

	got := utils.GetMessageType(msg)
	if got != "interactive response" {
		t.Fatalf("GetMessageType = %q, want \"interactive response\"", got)
	}
	if got == "ignore" || len(got) >= len("unknown_protocol_") && got[:len("unknown_protocol_")] == "unknown_protocol_" {
		t.Fatalf("interactive response would be dropped by the event handler: %q", got)
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}
