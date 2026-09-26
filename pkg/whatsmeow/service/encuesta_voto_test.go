package whatsmeow_service

// Ida y vuelta REAL del cifrado de un voto, con dos clientes whatsmeow sin
// red (nunca se conectan): «nosotros» enviamos la encuesta y guardamos su
// secreto; el «cliente» vota cifrando con su LID, como hace hoy el teléfono;
// descifrarVoto debe leerlo aunque evolution-go haya canjeado el LID por el
// número antes de llegar aquí.

import (
	"context"
	"reflect"
	"testing"

	poll_service "github.com/evolution-foundation/evolution-go/pkg/poll/service"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// secretosEnMemoria es un MsgSecretStore mínimo: busca por id de mensaje y
// devuelve el remitente con el que se guardó (como la tabla real).
type secretosEnMemoria struct {
	porID map[types.MessageID]struct {
		secreto []byte
		sender  types.JID
	}
}

func (m *secretosEnMemoria) PutMessageSecrets(ctx context.Context, ins []store.MessageSecretInsert) error {
	for _, i := range ins {
		_ = m.PutMessageSecret(ctx, i.Chat, i.Sender, i.ID, i.Secret)
	}
	return nil
}

func (m *secretosEnMemoria) PutMessageSecret(_ context.Context, _, sender types.JID, id types.MessageID, secreto []byte) error {
	if m.porID == nil {
		m.porID = map[types.MessageID]struct {
			secreto []byte
			sender  types.JID
		}{}
	}
	m.porID[id] = struct {
		secreto []byte
		sender  types.JID
	}{secreto, sender.ToNonAD()}
	return nil
}

func (m *secretosEnMemoria) GetMessageSecret(_ context.Context, _, _ types.JID, id types.MessageID) ([]byte, types.JID, error) {
	v, ok := m.porID[id]
	if !ok {
		return nil, types.EmptyJID, nil
	}
	return v.secreto, v.sender, nil
}

func clienteSinRed(pn, lid types.JID) *whatsmeow.Client {
	return whatsmeow.NewClient(&store.Device{ID: &pn, LID: lid, MsgSecrets: &secretosEnMemoria{}}, nil)
}

func TestVotoCifradoConLIDSeDescifraTrasElCanje(t *testing.T) {
	ctx := context.Background()
	nuestroPN := types.NewJID("51900000001", types.DefaultUserServer)
	nuestroLID := types.NewJID("111111111", types.HiddenUserServer)
	clientePN := types.NewJID("51999888777", types.DefaultUserServer)
	clienteLID := types.NewJID("222222222", types.HiddenUserServer)

	nosotros := clienteSinRed(nuestroPN, nuestroLID)
	cliente := clienteSinRed(clientePN, clienteLID)

	// La encuesta sale de nosotros al número del cliente: whatsmeow guarda el
	// secreto con sender = nosotros (send.go) y el teléfono del cliente lo
	// guarda al recibirla.
	const encuestaID = "3EB0ENCUESTA01"
	opciones := []string{"Bien", "Regular", "Mal"}
	secreto := random.Bytes(32)
	_ = nosotros.Store.MsgSecrets.PutMessageSecret(ctx, clientePN, nuestroPN, encuestaID, secreto)
	_ = cliente.Store.MsgSecrets.PutMessageSecret(ctx, nuestroLID, nuestroPN, encuestaID, secreto)

	// El cliente vota «Regular» en un chat direccionado por LID: firma con su LID.
	infoEncuesta := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: nuestroLID, Sender: nuestroLID},
		ID:            encuestaID,
	}
	voto, err := cliente.BuildPollVote(ctx, infoEncuesta, []string{"Regular"})
	if err != nil {
		t.Fatalf("no se pudo cifrar el voto: %v", err)
	}

	// Así llega a evolution-go: remitente LID con su número en SenderAlt.
	original := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: clienteLID, Sender: clienteLID, SenderAlt: clientePN},
		ID:            "3EB0VOTO01",
	}
	evt := &events.Message{Info: original, Message: voto}
	// …y así queda tras el canje de whatsmeow.go (Sender y Chat = número).
	evt.Info.Sender, evt.Info.SenderAlt, evt.Info.Chat = clientePN, clienteLID, clientePN

	if _, err := nosotros.DecryptPollVote(ctx, evt); err == nil {
		t.Fatal("con la info canjeada el voto NO debería descifrarse (si pasa, el canje dejó de importar y esta prueba sobra)")
	}
	descifrado, err := descifrarVoto(ctx, nosotros, evt, original)
	if err != nil {
		t.Fatalf("descifrarVoto debe leer el voto con la identidad original: %v", err)
	}
	nombres, desconocidos := poll_service.ResolverOpciones(descifrado.GetSelectedOptions(), opciones)
	if !reflect.DeepEqual(nombres, []string{"Regular"}) || len(desconocidos) != 0 {
		t.Fatalf("debió elegir «Regular»: %v %v", nombres, desconocidos)
	}

	datos := votoParaWebhook(encuestaID, descifrado, &poll_service.Encuesta{Pregunta: "¿Cómo te atendimos?", Opciones: opciones})
	if !reflect.DeepEqual(datos["selectedOptions"], []string{"Regular"}) || datos["question"] != "¿Cómo te atendimos?" {
		t.Fatalf("el webhook debe llevar el texto de la opción: %+v", datos)
	}
}

func TestVotoSinSecretoFallaConError(t *testing.T) {
	ctx := context.Background()
	nosotros := clienteSinRed(types.NewJID("51900000001", types.DefaultUserServer), types.EmptyJID)
	evt := &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{
			Chat: types.NewJID("51999888777", types.DefaultUserServer), Sender: types.NewJID("51999888777", types.DefaultUserServer),
		}},
		Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: nil, Vote: &waE2E.PollEncValue{EncPayload: []byte{1}, EncIV: []byte{2}},
		}},
	}
	if _, err := descifrarVoto(ctx, nosotros, evt, evt.Info); err == nil {
		t.Fatal("sin secreto guardado (encuesta enviada desde otro dispositivo) no hay voto")
	}
}

func TestVotoParaWebhookSinRegistroDejaHashes(t *testing.T) {
	voto := &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Sí"})}
	datos := votoParaWebhook("X", voto, nil)
	if len(datos["selectedOptions"].([]string)) != 0 || len(datos["selectedHashes"].([]string)) != 1 {
		t.Fatalf("sin registro: texto vacío y hash presente: %+v", datos)
	}
	datos = votoParaWebhook("X", voto, &poll_service.Encuesta{Opciones: []string{"No", "Talvez"}})
	if datos["unknownHashes"] == nil {
		t.Fatal("un hash que no está en la encuesta registrada se informa aparte")
	}
}
