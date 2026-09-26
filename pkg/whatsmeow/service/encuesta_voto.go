package whatsmeow_service

// Votos de encuesta (PollUpdateMessage). El voto viaja cifrado con una
// clave derivada del secreto de la encuesta (el que whatsmeow guardó en
// whatsmeow_message_secrets al ENVIARLA) + el id de la encuesta + el JID de
// quien la creó + el JID de quien vota. Descifrado, trae hashes SHA-256 del
// texto de las opciones elegidas.
//
// La trampa: evolution-go «canjea» Sender↔SenderAlt (LID → número) ANTES de
// llegar aquí, para que el webhook hable de números. Pero la clave del voto
// se derivó con el JID que el teléfono del cliente usó para firmar —hoy casi
// siempre su LID—, así que descifrar con el número canjeado da «message
// authentication failed». Por eso se prueba primero con la info ORIGINAL,
// luego con la canjeada y al final con Sender/SenderAlt cruzados.

import (
	"context"
	"encoding/hex"

	poll_service "github.com/evolution-foundation/evolution-go/pkg/poll/service"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type descifradorVotos interface {
	DecryptPollVote(ctx context.Context, vote *events.Message) (*waE2E.PollVoteMessage, error)
}

// descifrarVoto intenta con cada identidad posible del votante. Devuelve el
// primer error si ninguna sirve (el más informativo: el de la original).
func descifrarVoto(ctx context.Context, cli descifradorVotos, evt *events.Message, original types.MessageInfo) (*waE2E.PollVoteMessage, error) {
	intentos := []types.MessageInfo{original, evt.Info}
	if !original.SenderAlt.IsEmpty() {
		cruzada := original
		cruzada.Sender, cruzada.SenderAlt = original.SenderAlt, original.Sender
		intentos = append(intentos, cruzada)
	}
	var primero error
	vistos := map[string]bool{}
	for _, info := range intentos {
		k := info.Chat.String() + "|" + info.Sender.String()
		if vistos[k] {
			continue
		}
		vistos[k] = true
		copia := *evt
		copia.Info = info
		voto, err := cli.DecryptPollVote(ctx, &copia)
		if err == nil {
			return voto, nil
		}
		if primero == nil {
			primero = err
		}
	}
	return nil, primero
}

// votoParaWebhook arma data.pollVote: las opciones en TEXTO si la encuesta
// está registrada (poll_messages), y siempre los hashes en hex.
func votoParaWebhook(encuestaID string, voto *waE2E.PollVoteMessage, reg *poll_service.Encuesta) map[string]interface{} {
	hashes := make([]string, 0, len(voto.GetSelectedOptions()))
	for _, h := range voto.GetSelectedOptions() {
		hashes = append(hashes, hex.EncodeToString(h))
	}
	out := map[string]interface{}{
		"pollMessageId":   encuestaID,
		"selectedHashes":  hashes,
		"selectedOptions": []string{},
	}
	if reg != nil {
		nombres, desconocidos := poll_service.ResolverOpciones(voto.GetSelectedOptions(), reg.Opciones)
		out["selectedOptions"] = nombres
		out["question"] = reg.Pregunta
		out["options"] = reg.Opciones
		if len(desconocidos) > 0 {
			out["unknownHashes"] = desconocidos
		}
	}
	return out
}
