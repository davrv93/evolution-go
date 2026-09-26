package poll_service

// Registro de encuestas ENVIADAS (poll_messages): pregunta y texto de cada
// opción por id de mensaje. El voto llega como hashes SHA-256 de las
// opciones; sin este registro el webhook solo podría decir «eligió
// 9f86d0…», no «eligió Sí». Lo llena sendPoll al enviar (POST /send/poll y
// el paso encuesta de los flujos) y lo lee el manejador de votos.
//
// Las opciones se guardan como JSON en TEXT (no TEXT[] con comas a mano,
// como poll_votes): una opción «Sí, claro» rompería el arreglo.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Encuesta es lo que se registró al enviar.
type Encuesta struct {
	MessageID  string
	InstanceID string
	ChatJid    string
	Pregunta   string
	Opciones   []string
}

const crearPollMessages = `
	CREATE TABLE IF NOT EXISTS poll_messages (
		poll_message_id VARCHAR(255) NOT NULL,
		instance_id VARCHAR(255) NOT NULL,
		chat_jid VARCHAR(255) NOT NULL DEFAULT '',
		question TEXT NOT NULL DEFAULT '',
		options_json TEXT NOT NULL DEFAULT '[]',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (instance_id, poll_message_id)
	)`

func (s *pollService) migrarRegistro() error {
	if s.db == nil {
		return errors.New("poll: sin base")
	}
	_, err := s.db.Exec(crearPollMessages)
	return err
}

// RegistrarEncuesta guarda la encuesta enviada. Idempotente por
// (instancia, id de mensaje).
func (s *pollService) RegistrarEncuesta(ctx context.Context, e Encuesta) error {
	if s.db == nil || e.MessageID == "" || e.InstanceID == "" {
		return nil
	}
	ops, _ := json.Marshal(e.Opciones)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO poll_messages (poll_message_id, instance_id, chat_jid, question, options_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (instance_id, poll_message_id) DO NOTHING`,
		e.MessageID, e.InstanceID, e.ChatJid, e.Pregunta, string(ops), time.Now())
	if err != nil {
		return fmt.Errorf("poll: registrar encuesta: %w", err)
	}
	return nil
}

// EncuestaPorID devuelve la encuesta registrada, o nil si no está (una que
// no salió de esta instancia, o anterior al registro).
func (s *pollService) EncuestaPorID(ctx context.Context, instanceID, messageID string) (*Encuesta, error) {
	if s.db == nil {
		return nil, nil
	}
	var e Encuesta
	var ops string
	err := s.db.QueryRowContext(ctx, `
		SELECT poll_message_id, instance_id, chat_jid, question, options_json
		  FROM poll_messages WHERE instance_id = $1 AND poll_message_id = $2`,
		instanceID, messageID).Scan(&e.MessageID, &e.InstanceID, &e.ChatJid, &e.Pregunta, &ops)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(ops), &e.Opciones)
	return &e, nil
}

// ResolverOpciones traduce los hashes de un voto al texto de las opciones.
// Lo que no casa (encuesta ajena, opción editada) sale como hash en hex,
// para que el dato no se pierda.
func ResolverOpciones(hashes [][]byte, opciones []string) (nombres []string, desconocidos []string) {
	nombres = []string{}
	for _, h := range hashes {
		hallado := false
		for _, o := range opciones {
			suma := sha256.Sum256([]byte(o))
			if bytes.Equal(h, suma[:]) {
				nombres = append(nombres, o)
				hallado = true
				break
			}
		}
		if !hallado {
			desconocidos = append(desconocidos, hex.EncodeToString(h))
		}
	}
	return nombres, desconocidos
}
