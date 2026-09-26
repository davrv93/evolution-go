package send_service

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestCopiaPropiaDeUnaListaEsTexto(t *testing.T) {
	data := &ListStruct{Title: "Asistente de gerencia", Description: "🏪 Botica", FooterText: "Toca una opción", ButtonText: "Ver opciones",
		Sections: []Section{{Title: "Menú", Rows: []Row{{Title: "Ventas de hoy", RowId: "wa2g:1"}, {Title: "Reportes", Description: "Gráfico + Excel", RowId: "wa2g:8"}}}}}
	msg, _ := buildListMessage("viewonce", data, make([]byte, 32))
	got := whatsmeow.OwnDevicesMessage(types.NewJID("51999", types.DefaultUserServer), msg)
	if got == nil {
		t.Fatal("la copia propia de una lista debe ser texto")
	}
	txt := got.GetConversation()
	for _, want := range []string{"*Asistente de gerencia*", "🏪 Botica", "*1.* Ventas de hoy", "*2.* Reportes — Gráfico + Excel", "_Toca una opción_"} {
		if !strings.Contains(txt, want) {
			t.Errorf("falta %q en:\n%s", want, txt)
		}
	}
}

func TestCopiaPropiaDeBotonesEsTexto(t *testing.T) {
	data := &ButtonStruct{Title: "Menú", Description: "Hola", Footer: "Toca",
		Buttons: []Button{{Type: "reply", Id: "wa2n:1", DisplayText: "Stock"}, {Type: "reply", Id: "wa2n:2", DisplayText: "Pedido"}}}
	for _, estilo := range []string{"viewonce", "legacy"} {
		msg, _ := buildReplyButtonsMessage(estilo, data, make([]byte, 32), nil)
		got := whatsmeow.OwnDevicesMessage(types.JID{}, msg)
		if got == nil || !strings.Contains(got.GetConversation(), "*1.* Stock") || !strings.Contains(got.GetConversation(), "*2.* Pedido") {
			t.Fatalf("%s: %v", estilo, got)
		}
	}
}

func TestCopiaPropiaDeTextoNoCambia(t *testing.T) {
	if got := whatsmeow.OwnDevicesMessage(types.JID{}, &waE2E.Message{Conversation: proto.String("hola")}); got != nil {
		t.Fatalf("un texto normal no se toca: %v", got)
	}
}
