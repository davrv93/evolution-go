package send_service

import (
	"encoding/json"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Cuando este servidor manda una tarjeta con botones o lista, WhatsApp reenvía
// una copia a los OTROS dispositivos de la cuenta (el teléfono principal del
// negocio). La app personal no pinta esas tarjetas propias: el destinatario
// las ve y el remitente ve un hueco en su chat. Aquí la copia propia se
// convierte en texto: el mismo contenido con las opciones numeradas, que es lo
// que se le mandó al cliente. El destinatario no nota nada.
func init() {
	whatsmeow.OwnDevicesMessage = func(_ types.JID, msg *waE2E.Message) *waE2E.Message {
		t := textoDeInteractivo(msg)
		if t == "" {
			return nil
		}
		return &waE2E.Message{Conversation: proto.String(t)}
	}
}

// textoDeInteractivo devuelve el texto plano de un mensaje con botones o
// lista, venga como venga envuelto; "" si no es interactivo.
func textoDeInteractivo(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if im := interactiveOf(msg); im != nil {
		return textoNativeFlow(im)
	}
	m := msg
	if d := msg.GetDocumentWithCaptionMessage().GetMessage(); d != nil {
		m = d
	}
	if b := m.GetButtonsMessage(); b != nil {
		var ops []string
		for _, x := range b.GetButtons() {
			ops = append(ops, x.GetButtonText().GetDisplayText())
		}
		return armarTexto("", b.GetContentText(), b.GetFooterText(), ops)
	}
	if l := m.GetListMessage(); l != nil {
		var ops []string
		for _, s := range l.GetSections() {
			for _, r := range s.GetRows() {
				ops = append(ops, conDescripcion(r.GetTitle(), r.GetDescription()))
			}
		}
		return armarTexto(l.GetTitle(), l.GetDescription(), l.GetFooterText(), ops)
	}
	return ""
}

func textoNativeFlow(im *waE2E.InteractiveMessage) string {
	nf := im.GetNativeFlowMessage()
	if nf == nil {
		return ""
	}
	var ops []string
	for _, b := range nf.GetButtons() {
		var p struct {
			DisplayText string `json:"display_text"`
			Sections    []struct {
				Rows []struct {
					Title       string `json:"title"`
					Description string `json:"description"`
				} `json:"rows"`
			} `json:"sections"`
		}
		_ = json.Unmarshal([]byte(b.GetButtonParamsJSON()), &p)
		if b.GetName() == "single_select" {
			for _, s := range p.Sections {
				for _, r := range s.Rows {
					ops = append(ops, conDescripcion(r.Title, r.Description))
				}
			}
			continue
		}
		if p.DisplayText != "" {
			ops = append(ops, p.DisplayText)
		}
	}
	if len(ops) == 0 {
		return ""
	}
	return armarTexto(im.GetHeader().GetTitle(), im.GetBody().GetText(), im.GetFooter().GetText(), ops)
}

func conDescripcion(titulo, desc string) string {
	if strings.TrimSpace(desc) == "" {
		return titulo
	}
	return titulo + " — " + desc
}

func armarTexto(titulo, cuerpo, pie string, opciones []string) string {
	var b strings.Builder
	if t := strings.TrimSpace(titulo); t != "" {
		b.WriteString("*" + t + "*\n")
	}
	if c := strings.TrimSpace(cuerpo); c != "" {
		b.WriteString(c + "\n")
	}
	b.WriteString("\n")
	for i, o := range opciones {
		b.WriteString("*" + strconv.Itoa(i+1) + ".* " + o + "\n")
	}
	if p := strings.TrimSpace(pie); p != "" {
		b.WriteString("\n_" + p + "_")
	}
	return strings.TrimSpace(b.String())
}
