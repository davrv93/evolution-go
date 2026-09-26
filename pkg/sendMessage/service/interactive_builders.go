package send_service

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Construcción de los mensajes interactivos de /send/button (sólo `reply`) y
// /send/list. Son funciones puras: reciben los datos ya validados, el secreto
// del mensaje y, si lo hay, el medio de cabecera ya subido; no tocan el
// cliente. Así se prueban sin sesión de WhatsApp.
//
// Dos estilos, elegidos por INTERACTIVE_STYLE (pkg/config):
//
//   - legacy   → ButtonsMessage / ListMessage dentro de DocumentWithCaptionMessage,
//     con MessageContextInfo{MessageSecret} en la raíz. Es lo que el fork
//     mandaba hasta ahora y se conserva byte a byte.
//   - viewonce → ViewOnceMessage → Message{MessageContextInfo{DeviceListMetadata{},
//     DeviceListMetadataVersion:2, MessageSecret}, InteractiveMessage{Header?, Body,
//     Footer?, NativeFlowMessage{Buttons, MessageParamsJSON:""}}}. Es el árbol que
//     generan Baileys (`interactiveButtons`) y Evolution API v2, y el que los
//     clientes actuales de Android/iOS pintan desde cuentas no oficiales.

// Tipos de mensaje que SendMessage usa para colocar ContextInfo y en Info.Type.
const (
	msgTypeButtons     = "ButtonsMessage"
	msgTypeList        = "ListMessage"
	msgTypeInteractive = "InteractiveMessage"
)

// Nombres de flujo nativo que WhatsApp reconoce en NativeFlowButton.Name.
const (
	nativeFlowQuickReply   = "quick_reply"
	nativeFlowSingleSelect = "single_select"
)

// headerMedia es el adjunto de cabecera ya subido a los servidores de
// WhatsApp. `thumbnail` sólo lo usa el estilo viewonce (el legado nunca lo
// mandó y se mantiene igual).
type headerMedia struct {
	image     *waE2E.ImageMessage
	video     *waE2E.VideoMessage
	thumbnail []byte
}

func (m *headerMedia) present() bool {
	return m != nil && (m.image != nil || m.video != nil)
}

// newMessageSecret devuelve los 32 bytes aleatorios de MessageContextInfo.
// iOS no pinta mensajes interactivos sin él.
func newMessageSecret() []byte {
	secret := make([]byte, 32)
	_, _ = crypto_rand.Read(secret)
	return secret
}

// interactiveStyle devuelve el estilo efectivo de la configuración cargada;
// sin configuración (pruebas) o con valor raro, legado.
func (s *sendService) interactiveStyle() string {
	if s == nil || s.config == nil {
		return config.InteractiveStyleLegacy
	}
	style, _ := config.ParseInteractiveStyle(s.config.InteractiveStyle)
	return style
}

func isGroupNumber(number string) bool {
	return strings.Contains(number, "@g.us")
}

// ---------------------------------------------------------------------------
// Botones de respuesta (1–3 `reply`)
// ---------------------------------------------------------------------------

// buildReplyButtonsMessage arma el mensaje de botones `reply` en el estilo
// pedido. Devuelve el proto y el messageType que espera SendMessage.
func buildReplyButtonsMessage(style string, data *ButtonStruct, secret []byte, media *headerMedia) (*waE2E.Message, string) {
	if style == config.InteractiveStyleViewOnce {
		return buildReplyButtonsViewOnce(data, secret, media), msgTypeInteractive
	}
	return buildReplyButtonsLegacy(data, secret, media), msgTypeButtons
}

// buildReplyButtonsLegacy: ButtonsMessage envuelto en DocumentWithCaptionMessage
// (Baileys PR #36). Idéntico a lo que el fork enviaba antes del interruptor.
func buildReplyButtonsLegacy(data *ButtonStruct, secret []byte, media *headerMedia) *waE2E.Message {
	var replyButtons []*waE2E.ButtonsMessage_Button
	for _, v := range data.Buttons {
		replyButtons = append(replyButtons, &waE2E.ButtonsMessage_Button{
			ButtonID: proto.String(v.Id),
			ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{
				DisplayText: proto.String(v.DisplayText),
			},
			Type: waE2E.ButtonsMessage_Button_RESPONSE.Enum(),
		})
	}

	buttonsMsg := &waE2E.ButtonsMessage{
		ContentText: proto.String(data.Description),
		FooterText:  proto.String(data.Footer),
		HeaderType:  waE2E.ButtonsMessage_EMPTY.Enum(),
		Buttons:     replyButtons,
	}

	switch {
	case media != nil && media.image != nil:
		buttonsMsg.HeaderType = waE2E.ButtonsMessage_IMAGE.Enum()
		buttonsMsg.Header = &waE2E.ButtonsMessage_ImageMessage{ImageMessage: media.image}
	case media != nil && media.video != nil:
		buttonsMsg.HeaderType = waE2E.ButtonsMessage_VIDEO.Enum()
		buttonsMsg.Header = &waE2E.ButtonsMessage_VideoMessage{VideoMessage: media.video}
	}

	return &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ButtonsMessage: buttonsMsg,
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: secret,
		},
	}
}

// buildReplyButtonsViewOnce: lo que Baileys genera para `interactiveButtons`.
func buildReplyButtonsViewOnce(data *ButtonStruct, secret []byte, media *headerMedia) *waE2E.Message {
	interactive := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(data.Description)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: quickReplyButtons(data.Buttons),
				// Baileys manda la cadena vacía y NO pone messageVersion.
				MessageParamsJSON: proto.String(""),
			},
		},
	}
	if header := interactiveHeader(data.Title, media); header != nil {
		interactive.Header = header
	}
	if data.Footer != "" {
		interactive.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(data.Footer)}
	}
	return wrapViewOnceInteractive(interactive, secret)
}

// quickReplyButtons convierte cada botón `reply` en un NativeFlowButton
// quick_reply con {"display_text","id"}.
func quickReplyButtons(buttons []Button) []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton {
	out := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(buttons))
	for _, v := range buttons {
		params, _ := json.Marshal(struct {
			DisplayText string `json:"display_text"`
			ID          string `json:"id"`
		}{v.DisplayText, v.Id})
		out = append(out, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String(nativeFlowQuickReply),
			ButtonParamsJSON: proto.String(string(params)),
		})
	}
	return out
}

// interactiveHeader devuelve la cabecera del InteractiveMessage, o nil si no
// hay ni título ni medio (Baileys omite el header en ese caso).
func interactiveHeader(title string, media *headerMedia) *waE2E.InteractiveMessage_Header {
	hasMedia := media.present()
	if title == "" && !hasMedia {
		return nil
	}
	header := &waE2E.InteractiveMessage_Header{HasMediaAttachment: proto.Bool(hasMedia)}
	if title != "" {
		header.Title = proto.String(title)
	}
	switch {
	case media != nil && media.image != nil:
		img := proto.Clone(media.image).(*waE2E.ImageMessage)
		if img.JPEGThumbnail == nil && len(media.thumbnail) > 0 {
			img.JPEGThumbnail = media.thumbnail
		}
		header.Media = &waE2E.InteractiveMessage_Header_ImageMessage{ImageMessage: img}
	case media != nil && media.video != nil:
		header.Media = &waE2E.InteractiveMessage_Header_VideoMessage{VideoMessage: media.video}
	}
	return header
}

// wrapViewOnceInteractive envuelve el InteractiveMessage tal como Baileys:
// ViewOnceMessage → Message{MessageContextInfo, InteractiveMessage}. El
// MessageContextInfo va DENTRO del ViewOnce, no en la raíz.
func wrapViewOnceInteractive(interactive *waE2E.InteractiveMessage, secret []byte) *waE2E.Message {
	return &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				MessageContextInfo: &waE2E.MessageContextInfo{
					DeviceListMetadata:        &waE2E.DeviceListMetadata{},
					DeviceListMetadataVersion: proto.Int32(2),
					MessageSecret:             secret,
				},
				InteractiveMessage: interactive,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Listas (/send/list, selección única)
// ---------------------------------------------------------------------------

// buildListMessage arma la lista en el estilo pedido. Devuelve el proto y el
// messageType que espera SendMessage.
func buildListMessage(style string, data *ListStruct, secret []byte) (*waE2E.Message, string) {
	if style == config.InteractiveStyleViewOnce {
		return buildListViewOnce(data, secret), msgTypeInteractive
	}
	return buildListLegacy(data, secret), msgTypeList
}

// buildListLegacy: ListMessage en DocumentWithCaptionMessage, igual que antes
// del interruptor (mismo relleno " " y mismos ids de respaldo).
func buildListLegacy(data *ListStruct, secret []byte) *waE2E.Message {
	buttonText := data.ButtonText
	if buttonText == "" {
		buttonText = "Ver Menu"
	}

	var sections []*waE2E.ListMessage_Section
	for _, sec := range data.Sections {
		sectionTitle := sec.Title
		if sectionTitle == "" {
			sectionTitle = " "
		}
		var rows []*waE2E.ListMessage_Row
		for i, r := range sec.Rows {
			rowTitle := r.Title
			if rowTitle == "" {
				rowTitle = " "
			}
			rowId := r.RowId
			if rowId == "" {
				rowId = fmt.Sprintf("row_%d_%d", i, len(rows))
			}
			rows = append(rows, &waE2E.ListMessage_Row{
				Title:       proto.String(rowTitle),
				Description: proto.String(r.Description),
				RowID:       proto.String(rowId),
			})
		}
		sections = append(sections, &waE2E.ListMessage_Section{
			Title: proto.String(sectionTitle),
			Rows:  rows,
		})
	}

	listType := waE2E.ListMessage_SINGLE_SELECT
	listMessage := &waE2E.ListMessage{
		Title:       proto.String(data.Title),
		Description: proto.String(data.Description),
		ButtonText:  proto.String(buttonText),
		FooterText:  proto.String(data.FooterText),
		ListType:    &listType,
		Sections:    sections,
	}

	return &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ListMessage: listMessage,
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: secret,
		},
	}
}

// buildListViewOnce: un único NativeFlowButton single_select con las
// secciones en ButtonParamsJSON (Evolution API v2 / Baileys).
func buildListViewOnce(data *ListStruct, secret []byte) *waE2E.Message {
	interactive := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(data.Description)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
					Name:             proto.String(nativeFlowSingleSelect),
					ButtonParamsJSON: proto.String(singleSelectParamsJSON(data)),
				}},
				MessageParamsJSON: proto.String(""),
			},
		},
	}
	if data.Title != "" {
		interactive.Header = &waE2E.InteractiveMessage_Header{
			Title:              proto.String(data.Title),
			HasMediaAttachment: proto.Bool(false),
		}
	}
	if data.FooterText != "" {
		interactive.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(data.FooterText)}
	}
	return wrapViewOnceInteractive(interactive, secret)
}

// Forma del ButtonParamsJSON de single_select. El orden de las claves lo fija
// el struct; `header` va siempre, vacío, como lo manda Baileys.
type singleSelectRow struct {
	Header      string `json:"header"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ID          string `json:"id"`
}

type singleSelectSection struct {
	Title string            `json:"title"`
	Rows  []singleSelectRow `json:"rows"`
}

type singleSelectParams struct {
	Title    string                `json:"title"`
	Sections []singleSelectSection `json:"sections"`
}

// singleSelectParamsJSON serializa el botón que abre la lista y sus secciones.
// `title` es el texto del botón ("Ver Menu" si viene vacío); las filas sin
// rowId reciben row_<sección>_<fila>.
func singleSelectParamsJSON(data *ListStruct) string {
	buttonText := data.ButtonText
	if buttonText == "" {
		buttonText = "Ver Menu"
	}

	sections := make([]singleSelectSection, 0, len(data.Sections))
	for si, sec := range data.Sections {
		rows := make([]singleSelectRow, 0, len(sec.Rows))
		for ri, r := range sec.Rows {
			rowId := r.RowId
			if rowId == "" {
				rowId = fmt.Sprintf("row_%d_%d", si, ri)
			}
			rows = append(rows, singleSelectRow{
				Header:      "",
				Title:       r.Title,
				Description: r.Description,
				ID:          rowId,
			})
		}
		sections = append(sections, singleSelectSection{Title: sec.Title, Rows: rows})
	}

	// Structs planos de cadenas: Marshal no puede fallar.
	out, _ := json.Marshal(singleSelectParams{Title: buttonText, Sections: sections})
	return string(out)
}

// ---------------------------------------------------------------------------
// Nodos <biz>/<bot> de la estanza (van en SendRequestExtra.AdditionalNodes)
// ---------------------------------------------------------------------------

// nativeFlowBizNodes: <biz><interactive type="native_flow" v="1"><native_flow
// name="X"/></interactive></biz> y, en chats 1:1, <bot biz_bot="1"/>.
// whatsmeow no añade <biz> propio para InteractiveMessage (ni directo ni
// dentro de ViewOnce), así que éste es el único que viaja.
func nativeFlowBizNodes(flowName, number string) []waBinary.Node {
	nodes := []waBinary.Node{{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag: "interactive",
			Attrs: waBinary.Attrs{
				"type": "native_flow",
				"v":    "1",
			},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"name": flowName},
			}},
		}},
	}}
	return appendBotNode(nodes, number)
}

func appendBotNode(nodes []waBinary.Node, number string) []waBinary.Node {
	if isGroupNumber(number) {
		return nodes
	}
	return append(nodes, waBinary.Node{
		Tag:   "bot",
		Attrs: waBinary.Attrs{"biz_bot": "1"},
	})
}

// modernBizNodes es el <biz> que hoy hace VISIBLE un InteractiveMessage
// (botones quick_reply y listas single_select) en Android/iOS:
//
//	<biz actual_actors="2" host_storage="2" privacy_mode_ts="<unix>">
//	  <engagement customer_service_state="open" conversation_state="open"/>
//	  <interactive type="native_flow" v="1"><native_flow v="9" name="mixed"/></interactive>
//	</biz>
//
// Con el nodo anterior (native_flow sin v y con name quick_reply/
// single_select) el servidor aceptaba el mensaje y el teléfono NO pintaba la
// tarjeta: medido el 25-09-2026 (el titular pidió «muéstrame las opciones»
// siete veces sobre listas entregadas). Es el nodo que inyectan los forks de
// Baileys que sí se ven (Onigi v10.0.2, «fix invisible buttons/list»).
func modernBizNodes(number string, ahora time.Time) []waBinary.Node {
	nodes := []waBinary.Node{{
		Tag: "biz",
		Attrs: waBinary.Attrs{
			"actual_actors":   "2",
			"host_storage":    "2",
			"privacy_mode_ts": strconv.FormatInt(ahora.Unix(), 10),
		},
		Content: []waBinary.Node{
			{
				Tag:   "engagement",
				Attrs: waBinary.Attrs{"customer_service_state": "open", "conversation_state": "open"},
			},
			{
				Tag:   "interactive",
				Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
				Content: []waBinary.Node{{
					Tag:   "native_flow",
					Attrs: waBinary.Attrs{"v": "9", "name": "mixed"},
				}},
			},
		},
	}}
	return appendBotNode(nodes, number)
}

// replyButtonsBizNodes: legacy → native_flow name="quick_reply" (lo de
// siempre); viewonce → modernBizNodes.
func replyButtonsBizNodes(style, number string) []waBinary.Node {
	if style == config.InteractiveStyleViewOnce {
		return modernBizNodes(number, time.Now())
	}
	return nativeFlowBizNodes(nativeFlowQuickReply, number)
}

// listBizNodes: legacy → <biz><list v="2" type="single_select"/></biz>;
// viewonce → modernBizNodes. Ambos con <bot> en 1:1.
func listBizNodes(style, number string) []waBinary.Node {
	if style == config.InteractiveStyleViewOnce {
		return modernBizNodes(number, time.Now())
	}
	nodes := []waBinary.Node{{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag: "list",
			Attrs: waBinary.Attrs{
				"v":    "2",
				"type": "single_select",
			},
		}},
	}}
	return appendBotNode(nodes, number)
}

// interactiveOf localiza el InteractiveMessage venga directo, dentro de
// DocumentWithCaptionMessage (legacy pix/mixed) o dentro de ViewOnceMessage
// (estilo viewonce). SendMessage lo usa para colgar el ContextInfo citado.
func interactiveOf(msg *waE2E.Message) *waE2E.InteractiveMessage {
	switch {
	case msg == nil:
		return nil
	case msg.InteractiveMessage != nil:
		return msg.InteractiveMessage
	case msg.DocumentWithCaptionMessage != nil:
		return interactiveOf(msg.DocumentWithCaptionMessage.Message)
	case msg.ViewOnceMessage != nil:
		return interactiveOf(msg.ViewOnceMessage.Message)
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Cabecera multimedia (necesita cliente; separada de la construcción pura)
// ---------------------------------------------------------------------------

// uploadHeaderMedia descarga data.ImageUrl (o, si no hay, data.VideoUrl) y lo
// sube a WhatsApp. Cualquier fallo devuelve nil y el mensaje sale sin
// cabecera, como hacía el código anterior. Sólo se intenta la imagen si viene.
func uploadHeaderMedia(client *whatsmeow.Client, data *ButtonStruct) *headerMedia {
	if client == nil || data == nil {
		return nil
	}
	if data.ImageUrl != "" {
		fileData, ok := fetchURL(data.ImageUrl)
		if !ok {
			return nil
		}
		uploaded, err := client.Upload(context.Background(), fileData, whatsmeow.MediaImage)
		if err != nil {
			return nil
		}
		return &headerMedia{
			image: &waE2E.ImageMessage{
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String("image/jpeg"),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uint64(len(fileData))),
			},
			thumbnail: makeJPEGThumbnail(fileData, 72),
		}
	}
	if data.VideoUrl != "" {
		fileData, ok := fetchURL(data.VideoUrl)
		if !ok {
			return nil
		}
		uploaded, err := client.Upload(context.Background(), fileData, whatsmeow.MediaVideo)
		if err != nil {
			return nil
		}
		return &headerMedia{
			video: &waE2E.VideoMessage{
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String("video/mp4"),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uint64(len(fileData))),
			},
		}
	}
	return nil
}

func fetchURL(url string) ([]byte, bool) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	fileData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	return fileData, true
}
