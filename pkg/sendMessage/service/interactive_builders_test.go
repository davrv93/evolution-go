package send_service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	config "github.com/evolution-foundation/evolution-go/pkg/config"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Pruebas sin red ni cliente: las funciones de interactive_builders.go reciben
// datos, secreto y medio ya subido, y devuelven el proto y los nodos <biz>.

func testSecret() []byte {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	return secret
}

func threeReplyButtons() *ButtonStruct {
	return &ButtonStruct{
		Number:      "51999999999",
		Title:       "Confirmar pedido",
		Description: "¿Confirmas el pedido #123?",
		Footer:      "Botica Lima",
		Buttons: []Button{
			{Type: "reply", DisplayText: "Sí, confirmo", Id: "wa2_si"},
			{Type: "reply", DisplayText: "Cambiar", Id: "wa2_cambiar"},
			{Type: "reply", DisplayText: "Cancelar", Id: "wa2_no"},
		},
	}
}

func sampleList() *ListStruct {
	return &ListStruct{
		Number:      "51999999999",
		Title:       "Nuestros planes",
		Description: "Elige el plan",
		ButtonText:  "Ver planes",
		FooterText:  "Botica Lima",
		Sections: []Section{
			{Title: "Planes", Rows: []Row{
				{Title: "Básico", Description: "S/ 29,90", RowId: "wa2_basico"},
				{Title: "Pro", Description: "S/ 59,90"}, // sin rowId → respaldo
			}},
			{Title: "", Rows: []Row{
				{Title: "Hablar con alguien", RowId: "wa2_humano"},
			}},
		},
	}
}

func sampleImage() *waE2E.ImageMessage {
	return &waE2E.ImageMessage{
		URL:           proto.String("https://mmg.whatsapp.net/x"),
		DirectPath:    proto.String("/v/x"),
		MediaKey:      []byte{1, 2, 3},
		Mimetype:      proto.String("image/jpeg"),
		FileEncSHA256: []byte{4},
		FileSHA256:    []byte{5},
		FileLength:    proto.Uint64(1234),
	}
}

// ---------------------------------------------------------------------------
// Botones reply — estilo viewonce
// ---------------------------------------------------------------------------

func TestBuildReplyButtonsMessage_ViewOnce_Tree(t *testing.T) {
	data := threeReplyButtons()
	secret := testSecret()

	msg, msgType := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, data, secret, nil)

	if msgType != "InteractiveMessage" {
		t.Fatalf("msgType = %q, want InteractiveMessage", msgType)
	}
	if msg.DocumentWithCaptionMessage != nil || msg.ButtonsMessage != nil || msg.InteractiveMessage != nil {
		t.Fatalf("viewonce style must not leave legacy wrappers at the root: %v", msg)
	}
	if msg.MessageContextInfo != nil {
		t.Fatalf("MessageContextInfo must live inside ViewOnceMessage, not at the root (Baileys)")
	}
	if msg.ViewOnceMessage == nil || msg.ViewOnceMessage.Message == nil {
		t.Fatalf("expected ViewOnceMessage.Message, got %v", msg)
	}
	inner := msg.ViewOnceMessage.Message

	mci := inner.MessageContextInfo
	if mci == nil {
		t.Fatal("inner MessageContextInfo missing")
	}
	if mci.DeviceListMetadata == nil {
		t.Fatal("DeviceListMetadata must be present (empty) as Baileys sends it")
	}
	if mci.GetDeviceListMetadataVersion() != 2 {
		t.Fatalf("DeviceListMetadataVersion = %d, want 2", mci.GetDeviceListMetadataVersion())
	}
	if !bytes.Equal(mci.MessageSecret, secret) {
		t.Fatal("MessageSecret must be the secret passed in")
	}

	im := inner.InteractiveMessage
	if im == nil {
		t.Fatal("inner InteractiveMessage missing")
	}
	if im.GetHeader().GetTitle() != data.Title || im.GetHeader().GetHasMediaAttachment() {
		t.Fatalf("header = %v, want title %q without media", im.GetHeader(), data.Title)
	}
	if im.GetBody().GetText() != data.Description {
		t.Fatalf("body = %q, want %q", im.GetBody().GetText(), data.Description)
	}
	if im.GetFooter().GetText() != data.Footer {
		t.Fatalf("footer = %q, want %q", im.GetFooter().GetText(), data.Footer)
	}
	if im.ContextInfo != nil {
		t.Fatal("builder must not set ContextInfo; SendMessage does when quoting")
	}

	nf := im.GetNativeFlowMessage()
	if nf == nil {
		t.Fatal("expected NativeFlowMessage oneof")
	}
	if nf.MessageParamsJSON == nil || *nf.MessageParamsJSON != "" {
		t.Fatalf("MessageParamsJSON = %v, want present and empty (Baileys)", nf.MessageParamsJSON)
	}
	if nf.MessageVersion != nil {
		t.Fatalf("MessageVersion must be unset for quick_reply, got %d", *nf.MessageVersion)
	}
	if len(nf.Buttons) != 3 {
		t.Fatalf("buttons = %d, want 3", len(nf.Buttons))
	}
	for i, b := range nf.Buttons {
		if b.GetName() != "quick_reply" {
			t.Fatalf("button %d name = %q, want quick_reply", i, b.GetName())
		}
		var params map[string]string
		if err := json.Unmarshal([]byte(b.GetButtonParamsJSON()), &params); err != nil {
			t.Fatalf("button %d ButtonParamsJSON is not valid JSON: %v (%q)", i, err, b.GetButtonParamsJSON())
		}
		if len(params) != 2 || params["display_text"] != data.Buttons[i].DisplayText || params["id"] != data.Buttons[i].Id {
			t.Fatalf("button %d params = %v, want display_text=%q id=%q only", i, params, data.Buttons[i].DisplayText, data.Buttons[i].Id)
		}
	}
	// Orden exacto de claves que manda Baileys.
	if got, want := nf.Buttons[0].GetButtonParamsJSON(), `{"display_text":"Sí, confirmo","id":"wa2_si"}`; got != want {
		t.Fatalf("ButtonParamsJSON = %s, want %s", got, want)
	}
}

func TestBuildReplyButtonsMessage_ViewOnce_OmitsEmptyHeaderAndFooter(t *testing.T) {
	data := threeReplyButtons()
	data.Title = ""
	data.Footer = ""

	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, data, testSecret(), nil)
	im := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	if im.Header != nil {
		t.Fatalf("header must be omitted when title is empty and there is no media, got %v", im.Header)
	}
	if im.Footer != nil {
		t.Fatalf("footer must be omitted when empty, got %v", im.Footer)
	}
	if im.GetBody().GetText() != data.Description {
		t.Fatal("body must still carry the description")
	}
}

func TestBuildReplyButtonsMessage_ViewOnce_SingleButton(t *testing.T) {
	data := threeReplyButtons()
	data.Buttons = data.Buttons[:1]
	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, data, testSecret(), nil)
	nf := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetNativeFlowMessage()
	if len(nf.GetButtons()) != 1 {
		t.Fatalf("buttons = %d, want 1", len(nf.GetButtons()))
	}
}

func TestBuildReplyButtonsMessage_ViewOnce_ImageHeader(t *testing.T) {
	data := threeReplyButtons()
	original := sampleImage()
	media := &headerMedia{image: original, thumbnail: []byte{0xff, 0xd8}}

	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, data, testSecret(), media)
	header := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetHeader()
	if header == nil || !header.GetHasMediaAttachment() {
		t.Fatalf("header must declare HasMediaAttachment=true, got %v", header)
	}
	if header.GetTitle() != data.Title {
		t.Fatalf("header title = %q, want %q", header.GetTitle(), data.Title)
	}
	img := header.GetImageMessage()
	if img == nil {
		t.Fatal("header media must be the uploaded ImageMessage")
	}
	if img.GetURL() != original.GetURL() || !bytes.Equal(img.MediaKey, original.MediaKey) {
		t.Fatal("header ImageMessage must carry the uploaded fields")
	}
	if !bytes.Equal(img.JPEGThumbnail, media.thumbnail) {
		t.Fatal("viewonce header should carry the JPEG thumbnail")
	}
	if original.JPEGThumbnail != nil {
		t.Fatal("builder must not mutate the caller's ImageMessage")
	}
	if header.GetVideoMessage() != nil {
		t.Fatal("no video expected")
	}
}

func TestBuildReplyButtonsMessage_ViewOnce_VideoHeaderWithoutTitle(t *testing.T) {
	data := threeReplyButtons()
	data.Title = ""
	video := &waE2E.VideoMessage{URL: proto.String("https://mmg.whatsapp.net/v"), Mimetype: proto.String("video/mp4")}

	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, data, testSecret(), &headerMedia{video: video})
	header := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetHeader()
	if header == nil || !header.GetHasMediaAttachment() || header.GetVideoMessage() == nil {
		t.Fatalf("header with video must exist even without title, got %v", header)
	}
	if header.Title != nil {
		t.Fatal("empty title must not be set on the header")
	}
}

// ---------------------------------------------------------------------------
// Botones reply — estilo legacy: árbol idéntico al que se mandaba antes
// ---------------------------------------------------------------------------

// legacyReplyButtonsTree reproduce, línea a línea, lo que SendButton construía
// antes del interruptor (ButtonsMessage en DocumentWithCaptionMessage con
// MessageContextInfo{MessageSecret} en la raíz).
func legacyReplyButtonsTree(data *ButtonStruct, secret []byte, image *waE2E.ImageMessage) *waE2E.Message {
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
	if image != nil {
		buttonsMsg.HeaderType = waE2E.ButtonsMessage_IMAGE.Enum()
		buttonsMsg.Header = &waE2E.ButtonsMessage_ImageMessage{ImageMessage: image}
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

func TestBuildReplyButtonsMessage_Legacy_MatchesPreviousTree(t *testing.T) {
	data := threeReplyButtons()
	secret := testSecret()

	msg, msgType := buildReplyButtonsMessage(config.InteractiveStyleLegacy, data, secret, nil)
	if msgType != "ButtonsMessage" {
		t.Fatalf("msgType = %q, want ButtonsMessage", msgType)
	}
	want := legacyReplyButtonsTree(data, secret, nil)
	if !proto.Equal(msg, want) {
		t.Fatalf("legacy tree changed.\n got: %v\nwant: %v", msg, want)
	}
	if msg.ViewOnceMessage != nil {
		t.Fatal("legacy must not wrap in ViewOnceMessage")
	}
}

func TestBuildReplyButtonsMessage_Legacy_ImageHeaderUnchanged(t *testing.T) {
	data := threeReplyButtons()
	secret := testSecret()
	image := sampleImage()
	// El legado nunca mandó miniatura: aunque el helper la calcule, no entra.
	media := &headerMedia{image: image, thumbnail: []byte{0xff, 0xd8}}

	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleLegacy, data, secret, media)
	want := legacyReplyButtonsTree(data, secret, sampleImage())
	if !proto.Equal(msg, want) {
		t.Fatalf("legacy tree with image header changed.\n got: %v\nwant: %v", msg, want)
	}
}

func TestBuildReplyButtonsMessage_UnknownStyleFallsBackToLegacy(t *testing.T) {
	data := threeReplyButtons()
	secret := testSecret()
	msg, msgType := buildReplyButtonsMessage("whatever", data, secret, nil)
	if msgType != "ButtonsMessage" || !proto.Equal(msg, legacyReplyButtonsTree(data, secret, nil)) {
		t.Fatal("unknown style must behave as legacy")
	}
}

// ---------------------------------------------------------------------------
// Listas — estilo viewonce
// ---------------------------------------------------------------------------

type ssRow struct {
	Header      string `json:"header"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ID          string `json:"id"`
}

type ssSection struct {
	Title string  `json:"title"`
	Rows  []ssRow `json:"rows"`
}

type ssParams struct {
	Title    string      `json:"title"`
	Sections []ssSection `json:"sections"`
}

func TestBuildListMessage_ViewOnce_Tree(t *testing.T) {
	data := sampleList()
	secret := testSecret()

	msg, msgType := buildListMessage(config.InteractiveStyleViewOnce, data, secret)
	if msgType != "InteractiveMessage" {
		t.Fatalf("msgType = %q, want InteractiveMessage", msgType)
	}
	if msg.DocumentWithCaptionMessage != nil || msg.ListMessage != nil || msg.MessageContextInfo != nil {
		t.Fatalf("viewonce list must not carry legacy wrappers at the root: %v", msg)
	}
	inner := msg.GetViewOnceMessage().GetMessage()
	if inner == nil {
		t.Fatal("expected ViewOnceMessage.Message")
	}
	mci := inner.GetMessageContextInfo()
	if mci.GetDeviceListMetadata() == nil || mci.GetDeviceListMetadataVersion() != 2 || !bytes.Equal(mci.GetMessageSecret(), secret) {
		t.Fatalf("inner MessageContextInfo = %v, want DeviceListMetadata{}, version 2, secret", mci)
	}

	im := inner.GetInteractiveMessage()
	if im.GetHeader().GetTitle() != data.Title || im.GetHeader().GetHasMediaAttachment() {
		t.Fatalf("header = %v, want title %q", im.GetHeader(), data.Title)
	}
	if im.GetBody().GetText() != data.Description {
		t.Fatalf("body = %q, want %q", im.GetBody().GetText(), data.Description)
	}
	if im.GetFooter().GetText() != data.FooterText {
		t.Fatalf("footer = %q, want %q", im.GetFooter().GetText(), data.FooterText)
	}

	nf := im.GetNativeFlowMessage()
	if nf == nil {
		t.Fatal("expected NativeFlowMessage")
	}
	if nf.MessageParamsJSON == nil || *nf.MessageParamsJSON != "" || nf.MessageVersion != nil {
		t.Fatalf("MessageParamsJSON/MessageVersion = %v/%v, want \"\"/nil", nf.MessageParamsJSON, nf.MessageVersion)
	}
	if len(nf.Buttons) != 1 || nf.Buttons[0].GetName() != "single_select" {
		t.Fatalf("buttons = %v, want exactly one single_select", nf.Buttons)
	}

	var params ssParams
	if err := json.Unmarshal([]byte(nf.Buttons[0].GetButtonParamsJSON()), &params); err != nil {
		t.Fatalf("ButtonParamsJSON is not valid JSON: %v", err)
	}
	if params.Title != "Ver planes" {
		t.Fatalf("params.title = %q, want the buttonText", params.Title)
	}
	if len(params.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(params.Sections))
	}
	sec := params.Sections[0]
	if sec.Title != "Planes" || len(sec.Rows) != 2 {
		t.Fatalf("section 0 = %+v", sec)
	}
	if sec.Rows[0] != (ssRow{Header: "", Title: "Básico", Description: "S/ 29,90", ID: "wa2_basico"}) {
		t.Fatalf("row 0 = %+v", sec.Rows[0])
	}
	if sec.Rows[1].ID != "row_0_1" {
		t.Fatalf("row without rowId must get row_<section>_<row>, got %q", sec.Rows[1].ID)
	}
	if params.Sections[1].Title != "" || params.Sections[1].Rows[0].ID != "wa2_humano" {
		t.Fatalf("section 1 = %+v", params.Sections[1])
	}
}

func TestBuildListMessage_ViewOnce_DefaultsAndOmissions(t *testing.T) {
	data := sampleList()
	data.Title = ""
	data.FooterText = ""
	data.ButtonText = ""

	msg, _ := buildListMessage(config.InteractiveStyleViewOnce, data, testSecret())
	im := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	if im.Header != nil || im.Footer != nil {
		t.Fatalf("empty title/footer must be omitted, got header=%v footer=%v", im.Header, im.Footer)
	}
	var params ssParams
	_ = json.Unmarshal([]byte(im.GetNativeFlowMessage().GetButtons()[0].GetButtonParamsJSON()), &params)
	if params.Title != "Ver Menu" {
		t.Fatalf("empty buttonText must fall back to \"Ver Menu\", got %q", params.Title)
	}
}

func TestSingleSelectParamsJSON_KeyOrder(t *testing.T) {
	data := &ListStruct{Sections: []Section{{Title: "S", Rows: []Row{{Title: "A"}}}}}
	got := singleSelectParamsJSON(data)
	want := `{"title":"Ver Menu","sections":[{"title":"S","rows":[{"header":"","title":"A","description":"","id":"row_0_0"}]}]}`
	if got != want {
		t.Fatalf("singleSelectParamsJSON =\n%s\nwant\n%s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Listas — estilo legacy: árbol idéntico al que se mandaba antes
// ---------------------------------------------------------------------------

// legacyListTree reproduce lo que SendList construía antes del interruptor,
// incluidos los rellenos " " y los ids de respaldo row_%d_%d.
func legacyListTree(data *ListStruct, secret []byte) *waE2E.Message {
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
	return &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ListMessage: &waE2E.ListMessage{
					Title:       proto.String(data.Title),
					Description: proto.String(data.Description),
					ButtonText:  proto.String(buttonText),
					FooterText:  proto.String(data.FooterText),
					ListType:    &listType,
					Sections:    sections,
				},
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: secret,
		},
	}
}

func TestBuildListMessage_Legacy_MatchesPreviousTree(t *testing.T) {
	for _, data := range []*ListStruct{
		sampleList(),
		{Number: "1", Sections: []Section{{Rows: []Row{{}, {}}}}}, // todo vacío: rellenos " " y row_1_1
	} {
		secret := testSecret()
		msg, msgType := buildListMessage(config.InteractiveStyleLegacy, data, secret)
		if msgType != "ListMessage" {
			t.Fatalf("msgType = %q, want ListMessage", msgType)
		}
		want := legacyListTree(data, secret)
		if !proto.Equal(msg, want) {
			t.Fatalf("legacy list tree changed.\n got: %v\nwant: %v", msg, want)
		}
		if msg.ViewOnceMessage != nil {
			t.Fatal("legacy list must not wrap in ViewOnceMessage")
		}
	}
}

// ---------------------------------------------------------------------------
// Nodos <biz>/<bot>
// ---------------------------------------------------------------------------

func nativeFlowBiz(name string) waBinary.Node {
	return waBinary.Node{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag:   "interactive",
			Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"name": name},
			}},
		}},
	}
}

var botNode = waBinary.Node{Tag: "bot", Attrs: waBinary.Attrs{"biz_bot": "1"}}

func TestReplyButtonsBizNodes(t *testing.T) {
	got := replyButtonsBizNodes("51999999999@s.whatsapp.net")
	want := []waBinary.Node{nativeFlowBiz("quick_reply"), botNode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("1:1 biz nodes = %#v, want %#v", got, want)
	}

	got = replyButtonsBizNodes("120363000000000000@g.us")
	want = []waBinary.Node{nativeFlowBiz("quick_reply")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("group biz nodes = %#v, want %#v (no <bot>)", got, want)
	}
}

func TestListBizNodes_Legacy(t *testing.T) {
	got := listBizNodes(config.InteractiveStyleLegacy, "51999999999")
	want := []waBinary.Node{
		{
			Tag: "biz",
			Content: []waBinary.Node{{
				Tag:   "list",
				Attrs: waBinary.Attrs{"v": "2", "type": "single_select"},
			}},
		},
		botNode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy list biz nodes = %#v, want %#v", got, want)
	}
	if n := len(listBizNodes(config.InteractiveStyleLegacy, "x@g.us")); n != 1 {
		t.Fatalf("group must skip <bot>, got %d nodes", n)
	}
}

func TestListBizNodes_ViewOnce(t *testing.T) {
	got := listBizNodes(config.InteractiveStyleViewOnce, "51999999999")
	want := []waBinary.Node{nativeFlowBiz("single_select"), botNode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("viewonce list biz nodes = %#v, want %#v", got, want)
	}
	if n := len(listBizNodes(config.InteractiveStyleViewOnce, "x@g.us")); n != 1 {
		t.Fatalf("group must skip <bot>, got %d nodes", n)
	}
}

// ---------------------------------------------------------------------------
// interactiveOf y selección de estilo
// ---------------------------------------------------------------------------

func TestInteractiveOf(t *testing.T) {
	im := &waE2E.InteractiveMessage{Body: &waE2E.InteractiveMessage_Body{Text: proto.String("x")}}

	cases := map[string]*waE2E.Message{
		"direct":   {InteractiveMessage: im},
		"document": {DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{InteractiveMessage: im}}},
		"viewonce": {ViewOnceMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{InteractiveMessage: im}}},
	}
	for name, msg := range cases {
		if got := interactiveOf(msg); got != im {
			t.Fatalf("%s: interactiveOf returned %v, want the embedded InteractiveMessage", name, got)
		}
	}
	if interactiveOf(nil) != nil {
		t.Fatal("nil message must yield nil")
	}
	if interactiveOf(&waE2E.Message{Conversation: proto.String("hi")}) != nil {
		t.Fatal("plain text must yield nil")
	}
	if interactiveOf(&waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{}}) != nil {
		t.Fatal("empty ViewOnce must yield nil, not panic")
	}
}

func TestInteractiveOf_QuotedContextLandsInsideViewOnce(t *testing.T) {
	msg, _ := buildReplyButtonsMessage(config.InteractiveStyleViewOnce, threeReplyButtons(), testSecret(), nil)
	interactiveOf(msg).ContextInfo = &waE2E.ContextInfo{StanzaID: proto.String("ABC")}
	if got := msg.GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetContextInfo().GetStanzaID(); got != "ABC" {
		t.Fatalf("quoted ContextInfo not reachable through interactiveOf: %q", got)
	}
}

func TestInteractiveStyle_FromConfig(t *testing.T) {
	if got := (&sendService{}).interactiveStyle(); got != config.InteractiveStyleLegacy {
		t.Fatalf("nil config must default to legacy, got %q", got)
	}
	svc := &sendService{config: &config.Config{InteractiveStyle: config.InteractiveStyleViewOnce}}
	if got := svc.interactiveStyle(); got != config.InteractiveStyleViewOnce {
		t.Fatalf("config viewonce must be honoured, got %q", got)
	}
	svc = &sendService{config: &config.Config{InteractiveStyle: "garbage"}}
	if got := svc.interactiveStyle(); got != config.InteractiveStyleLegacy {
		t.Fatalf("invalid config value must fall back to legacy, got %q", got)
	}
}

func TestNewMessageSecretIs32RandomBytes(t *testing.T) {
	a, b := newMessageSecret(), newMessageSecret()
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("secret lengths = %d/%d, want 32", len(a), len(b))
	}
	if bytes.Equal(a, b) {
		t.Fatal("two secrets must differ")
	}
}

func TestUploadHeaderMedia_NilClientOrNoURL(t *testing.T) {
	if uploadHeaderMedia(nil, threeReplyButtons()) != nil {
		t.Fatal("nil client must yield no media, not a panic")
	}
}
