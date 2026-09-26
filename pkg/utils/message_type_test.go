package utils

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// La respuesta de un botón nativo (quick_reply / single_select) llega como
// InteractiveResponseMessage. Debe clasificarse como mensaje propio, nunca
// como "ignore" ni "unknown_protocol_*", que es lo que el manejador descarta.
func TestGetMessageType_InteractiveResponse(t *testing.T) {
	msg := &waE2E.Message{
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Plan básico")},
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					Name:       proto.String("single_select"),
					ParamsJSON: proto.String(`{"id":"wa2_row_1","title":"Plan básico","description":"S/ 29,90"}`),
					Version:    proto.Int32(1),
				},
			},
		},
	}

	got := GetMessageType(msg)
	if got != "interactive response" {
		t.Fatalf("GetMessageType = %q, want \"interactive response\"", got)
	}
	if got == "ignore" || strings.HasPrefix(got, "unknown_protocol_") {
		t.Fatalf("interactive response would be discarded: %q", got)
	}
}
