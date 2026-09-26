package flow_service

// Paso «encuesta»: una encuesta NATIVA de WhatsApp (PollCreationMessage),
// una pregunta y de 2 a 12 opciones, selección única. El cliente vota con un
// toque y el voto llega CIFRADO (PollUpdateMessage): whatsmeow lo descifra
// con el secreto que guardó al enviar (whatsmeow_message_secrets) y lo que
// sale son hashes SHA-256 del texto de cada opción, no el texto. Por eso:
//
//   - las opciones de una encuesta NO pasan por plantilla(): el hash se
//     calcula sobre el texto literal que viajó y cualquier variación lo
//     rompería;
//   - el motor compara los hashes con sha256(etiqueta) de sus opciones; no
//     depende del registro de encuestas de pkg/poll (que sirve al webhook).
//
// Lo que el cliente ESCRIBE mientras el run espera el voto también vale: la
// etiqueta de una opción o su número (1..N). Así una app vieja que no pinta
// encuestas no deja la conversación trabada.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

const TipoEncuesta = "encuesta"

// Topes de WhatsApp para encuestas: 12 opciones como máximo (la app no deja
// crear más), pregunta de hasta 255 caracteres y opción de hasta 100.
const (
	EncuestaMinOpciones = 2
	EncuestaMaxOpciones = 12
	encuestaMaxPregunta = 255
	encuestaMaxOpcion   = 100
)

// TTLEncuesta: una encuesta saliente se contesta cuando el cliente abre el
// chat, a veces al día siguiente. Un run parado en una encuesta no se
// abandona a los 30 minutos del TTL normal, sino a las 72 horas.
const TTLEncuesta = 72 * time.Hour

// SenderEncuesta es el envío de encuestas. Es una interfaz aparte de Sender a
// propósito: quien no la implemente (un Sender viejo, un falso de pruebas)
// sigue compilando y la encuesta degrada a texto numerado.
type SenderEncuesta interface {
	// Encuesta envía la encuesta nativa y devuelve el id del mensaje (lo
	// trae de vuelta el voto en PollCreationMessageKey).
	Encuesta(inst *instance_model.Instance, numero, pregunta string, opciones []string) (string, error)
}

// Votante es lo que whatsmeow_service usa para entregar un voto descifrado.
type Votante interface {
	Votar(ctx context.Context, inst *instance_model.Instance, remitente, encuestaID string, hashes [][]byte) (bool, error)
}

// HashOpcion es sha256 del texto EXACTO de la opción: lo mismo que
// whatsmeow.HashPollOptions y lo que viaja en PollVoteMessage.SelectedOptions.
func HashOpcion(etiqueta string) []byte {
	h := sha256.Sum256([]byte(etiqueta))
	return h[:]
}

func claveEncuesta(paso string) string { return "encuesta_" + paso }
func claveOpcion(paso string) string   { return "opcion_" + paso }

// ── Validación ──────────────────────────────────────────────────────────────

func validarEncuesta(p *Paso, donde string, irA func(clave, donde string) error) error {
	cabe := func(s string, max int) bool { return len([]rune(strings.TrimSpace(s))) <= max }
	if strings.TrimSpace(p.Texto) == "" {
		return fmt.Errorf("flujo: %s sin pregunta", donde)
	}
	if !cabe(p.Texto, encuestaMaxPregunta) {
		return fmt.Errorf("flujo: %s con pregunta de más de %d caracteres", donde, encuestaMaxPregunta)
	}
	if len(p.Opciones) < EncuestaMinOpciones || len(p.Opciones) > EncuestaMaxOpciones {
		return fmt.Errorf("flujo: %s lleva de %d a %d opciones (tope de WhatsApp)", donde, EncuestaMinOpciones, EncuestaMaxOpciones)
	}
	ids := map[string]bool{}
	etiquetas := map[string]bool{}
	for _, o := range p.Opciones {
		id, et := strings.TrimSpace(o.Id), strings.TrimSpace(o.Etiqueta)
		if id == "" || et == "" {
			return fmt.Errorf("flujo: %s tiene opción sin id o texto", donde)
		}
		if ids[id] {
			return fmt.Errorf("flujo: %s repite el id de opción %q", donde, id)
		}
		ids[id] = true
		// Dos opciones con el mismo texto dan el mismo hash: el voto no
		// sabría a cuál ir. Se compara sin mayúsculas ni espacios de más.
		if etiquetas[normalizar(et)] {
			return fmt.Errorf("flujo: %s repite la opción %q", donde, et)
		}
		etiquetas[normalizar(et)] = true
		if !cabe(et, encuestaMaxOpcion) {
			return fmt.Errorf("flujo: opción %q de %s con más de %d caracteres", id, donde, encuestaMaxOpcion)
		}
		if strings.Contains(et, "{{") {
			return fmt.Errorf("flujo: opción %q de %s no admite {{variables}} (el voto llega por el texto exacto)", id, donde)
		}
		if o.IrA == "" {
			return fmt.Errorf("flujo: opción %q de %s sin destino", id, donde)
		}
		if err := irA(o.IrA, donde); err != nil {
			return err
		}
	}
	return nil
}

func resumenEncuesta(p *Paso) string {
	etiquetas := make([]string, 0, len(p.Opciones))
	for _, o := range p.Opciones {
		etiquetas = append(etiquetas, o.Etiqueta)
	}
	return fmt.Sprintf("Encuesta: %s [%s]", p.Texto, strings.Join(etiquetas, " | "))
}

// ── Envío ───────────────────────────────────────────────────────────────────

func etiquetasEncuesta(p *Paso) []string {
	out := make([]string, 0, len(p.Opciones))
	for _, o := range p.Opciones {
		out = append(out, strings.TrimSpace(o.Etiqueta))
	}
	return out
}

func textoNumerado(p *Paso, cabecera string) string {
	var b strings.Builder
	b.WriteString(cabecera)
	for i, o := range p.Opciones {
		fmt.Fprintf(&b, "\n%d. %s", i+1, strings.TrimSpace(o.Etiqueta))
	}
	return b.String()
}

// enviarEncuesta manda la encuesta y deja su id en el contexto del run
// (encuesta_<clave>): el voto se casa con ESA encuesta y no con una vieja.
// Sin SenderEncuesta, sale como texto numerado y se responde escribiendo.
func (s *flowService) enviarEncuesta(inst *instance_model.Instance, run *flow_model.FlowRun, paso *Paso) error {
	vars := mapaContexto(run.Contexto)
	pregunta := plantilla(paso.Texto, vars)
	if paso.Icono != "" && !strings.HasPrefix(pregunta, paso.Icono) {
		pregunta = paso.Icono + " " + pregunta
	}
	if se, ok := s.sender.(SenderEncuesta); ok {
		id, err := se.Encuesta(inst, run.Remitente, pregunta, etiquetasEncuesta(paso))
		if err != nil {
			return err
		}
		vars[claveEncuesta(paso.Clave)] = id
		run.Contexto = guardarContexto(vars)
		s.bitacora("encuesta %s enviada a %s (mensaje %s)", paso.Clave, run.Remitente, id)
		return nil
	}
	return s.sender.Texto(inst, run.Remitente, textoNumerado(paso, pregunta+"\nResponde con el número:"))
}

// ── Respuesta ───────────────────────────────────────────────────────────────

func opcionPorHash(paso *Paso, hashes [][]byte) *Opcion {
	for _, h := range hashes {
		for i := range paso.Opciones {
			if bytes.Equal(h, HashOpcion(strings.TrimSpace(paso.Opciones[i].Etiqueta))) ||
				bytes.Equal(h, HashOpcion(paso.Opciones[i].Etiqueta)) {
				return &paso.Opciones[i]
			}
		}
	}
	return nil
}

// opcionEncuestaPorTexto acepta la etiqueta (sin mayúsculas) o el número.
func opcionEncuestaPorTexto(paso *Paso, texto string) *Opcion {
	t := normalizar(texto)
	if t == "" {
		return nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(t, ".")); err == nil && n >= 1 && n <= len(paso.Opciones) {
		return &paso.Opciones[n-1]
	}
	for i := range paso.Opciones {
		if normalizar(paso.Opciones[i].Etiqueta) == t {
			return &paso.Opciones[i]
		}
	}
	return nil
}

// elegirEncuesta guarda la elección (opcion_<clave> = id, como botones y
// lista: el reporte las agrega igual) y sigue al destino de la opción.
func (s *flowService) elegirEncuesta(ctx context.Context, inst *instance_model.Instance, rec *flow_model.FlowDef, def *Definicion, run *flow_model.FlowRun, paso *Paso, op *Opcion) error {
	vars := mapaContexto(run.Contexto)
	vars[claveOpcion(paso.Clave)] = op.Id
	run.Contexto = guardarContexto(vars)
	s.bitacora("voto %s=%s de %s en flujo %s", paso.Clave, op.Id, run.Remitente, run.FlowID)
	run.StepActual = op.IrA
	return s.ejecutarDesde(ctx, inst, rec, def, run, "", "")
}

// continuarEncuesta atiende TEXTO mientras el run espera un voto.
func (s *flowService) continuarEncuesta(ctx context.Context, inst *instance_model.Instance, rec *flow_model.FlowDef, def *Definicion, run *flow_model.FlowRun, paso *Paso, texto string) (bool, error) {
	if op := opcionEncuestaPorTexto(paso, texto); op != nil {
		return true, s.elegirEncuesta(ctx, inst, rec, def, run, paso, op)
	}
	// No se reenvía la encuesta (sería insistir): se recuerda cómo contestar.
	msg := plantilla(paso.MensajeError, mapaContexto(run.Contexto))
	if strings.TrimSpace(msg) == "" {
		msg = "Para responder, toca una opción de la encuesta o escribe su número:"
	}
	if err := s.sender.Texto(inst, run.Remitente, textoNumerado(paso, msg)); err != nil {
		return true, err
	}
	tocarRun(run)
	return true, s.repo.GuardarRun(ctx, run)
}

// defActivaDe devuelve la definición del run si su flujo sigue activo.
func (s *flowService) defActivaDe(ctx context.Context, inst *instance_model.Instance, run *flow_model.FlowRun) (*flow_model.FlowDef, *Definicion) {
	defs, err := s.repo.DefsPorInstancia(ctx, inst.Id)
	if err != nil {
		return nil, nil
	}
	for i := range defs {
		if defs[i].Id == run.FlowID && defs[i].Estado == flow_model.EstadoActivo {
			var def Definicion
			if err := json.Unmarshal(defs[i].Steps, &def); err != nil {
				return nil, nil
			}
			return &defs[i], &def
		}
	}
	return nil, nil
}

// vencido dice si el run superó su TTL. Parado en una encuesta, el plazo es
// TTLEncuesta; en cualquier otro paso, el TTL normal del motor.
func (s *flowService) vencido(ctx context.Context, inst *instance_model.Instance, run *flow_model.FlowRun) bool {
	quieto := time.Since(run.UpdatedAt)
	if quieto <= s.ttl {
		return false
	}
	if quieto > TTLEncuesta {
		return true
	}
	if _, def := s.defActivaDe(ctx, inst, run); def != nil {
		if p := buscarPaso(def, run.StepActual); p != nil && p.Tipo == TipoEncuesta {
			return false
		}
	}
	return true
}

// Votar entrega un voto descifrado. Devuelve true si el motor lo usó.
//   - run parado en la encuesta votada → guarda y avanza;
//   - run ya más adelante (el cliente CAMBIÓ su voto) → solo actualiza
//     opcion_<clave>: el camino ya se tomó, el resultado refleja el último;
//   - voto a otra encuesta, hash desconocido o sin run → se ignora.
func (s *flowService) Votar(ctx context.Context, inst *instance_model.Instance, remitente, encuestaID string, hashes [][]byte) (bool, error) {
	if inst == nil || remitente == "" || len(hashes) == 0 {
		return false, nil
	}
	run, err := s.repo.RunActivo(ctx, inst.Id, remitente)
	if err != nil || run == nil {
		return false, nil
	}
	if s.vencido(ctx, inst, run) {
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		s.bitacora("run %s vencido (%s), el voto llegó tarde", run.Id, remitente)
		return false, nil
	}
	rec, def := s.defActivaDe(ctx, inst, run)
	if def == nil {
		return false, nil
	}
	vars := mapaContexto(run.Contexto)
	deEstaEncuesta := func(p *Paso) bool {
		enviada := vars[claveEncuesta(p.Clave)]
		return encuestaID == "" || enviada == "" || enviada == encuestaID
	}
	paso := buscarPaso(def, run.StepActual)
	if paso != nil && paso.Tipo == TipoEncuesta && deEstaEncuesta(paso) {
		op := opcionPorHash(paso, hashes)
		if op == nil {
			return false, nil
		}
		return true, s.elegirEncuesta(ctx, inst, rec, def, run, paso, op)
	}
	if encuestaID == "" {
		return false, nil
	}
	for i := range def.Pasos {
		p := &def.Pasos[i]
		if p.Tipo != TipoEncuesta || vars[claveEncuesta(p.Clave)] != encuestaID {
			continue
		}
		op := opcionPorHash(p, hashes)
		if op == nil || vars[claveOpcion(p.Clave)] == op.Id {
			return false, nil
		}
		vars[claveOpcion(p.Clave)] = op.Id
		run.Contexto = guardarContexto(vars)
		s.bitacora("voto cambiado %s=%s de %s en flujo %s", p.Clave, op.Id, remitente, run.FlowID)
		return true, s.repo.GuardarRun(ctx, run)
	}
	return false, nil
}

// ── Resultados ──────────────────────────────────────────────────────────────

type VotanteResultado struct {
	Remitente string    `json:"remitente"`
	At        time.Time `json:"at"`
	Estado    string    `json:"estado"`
}

type OpcionResultado struct {
	Id       string             `json:"id"`
	Etiqueta string             `json:"etiqueta"`
	Votos    int                `json:"votos"`
	Votantes []VotanteResultado `json:"votantes"`
}

type PreguntaResultado struct {
	Clave    string `json:"clave"`
	Tipo     string `json:"tipo"`
	Pregunta string `json:"pregunta"`
	// Enviadas: runs a los que salió la encuesta (solo tipo encuesta; en
	// botones/lista el motor no guarda el envío y queda en 0).
	Enviadas   int               `json:"enviadas"`
	Respuestas int               `json:"respuestas"`
	Opciones   []OpcionResultado `json:"opciones"`
}

// maxVotantesPorOpcion acota el JSON: el conteo es exacto, la lista no.
const maxVotantesPorOpcion = 200

// Resultados agrega, por cada paso con alternativas (encuesta, botones,
// lista), cuántos eligieron cada opción y quiénes. Lee opcion_<clave> del
// contexto de cada run: función pura, sin base ni red.
func Resultados(def Definicion, runs []flow_model.FlowRun) []PreguntaResultado {
	out := []PreguntaResultado{}
	ordenados := append([]flow_model.FlowRun(nil), runs...)
	sort.SliceStable(ordenados, func(i, j int) bool { return ordenados[i].UpdatedAt.After(ordenados[j].UpdatedAt) })
	contextos := make([]map[string]string, len(ordenados))
	for i := range ordenados {
		contextos[i] = mapaContexto(ordenados[i].Contexto)
	}
	for _, p := range def.Pasos {
		if p.Tipo != TipoEncuesta && p.Tipo != TipoBotones && p.Tipo != TipoLista {
			continue
		}
		pr := PreguntaResultado{Clave: p.Clave, Tipo: p.Tipo, Pregunta: p.Texto, Opciones: []OpcionResultado{}}
		indice := map[string]int{}
		agregar := func(id, etiqueta string) {
			indice[id] = len(pr.Opciones)
			pr.Opciones = append(pr.Opciones, OpcionResultado{Id: id, Etiqueta: etiqueta, Votantes: []VotanteResultado{}})
		}
		switch p.Tipo {
		case TipoEncuesta:
			for _, o := range p.Opciones {
				agregar(o.Id, o.Etiqueta)
			}
		case TipoBotones:
			for _, o := range p.Botones {
				agregar(o.Id, o.Etiqueta)
			}
		case TipoLista:
			for _, sec := range p.Secciones {
				for _, f := range sec.Filas {
					agregar(f.Id, f.Titulo)
				}
			}
		}
		for i, vars := range contextos {
			if vars[claveEncuesta(p.Clave)] != "" {
				pr.Enviadas++
			}
			id := vars[claveOpcion(p.Clave)]
			k, ok := indice[id]
			if id == "" || !ok {
				continue
			}
			pr.Respuestas++
			o := &pr.Opciones[k]
			o.Votos++
			if len(o.Votantes) < maxVotantesPorOpcion {
				o.Votantes = append(o.Votantes, VotanteResultado{
					Remitente: ordenados[i].Remitente, At: ordenados[i].UpdatedAt, Estado: ordenados[i].Estado,
				})
			}
		}
		out = append(out, pr)
	}
	return out
}
