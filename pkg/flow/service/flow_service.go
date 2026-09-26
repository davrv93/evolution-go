package flow_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	flow_repository "github.com/evolution-foundation/evolution-go/pkg/flow/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// TTLRun: sin moverse este tiempo, la conversación se marca abandonada y el
// siguiente mensaje vuelve a buscar entrada (no retoma a mitad de nada).
const TTLRun = 30 * time.Minute

// Tope de pasos encadenados por mensaje (condiciones en cadena): corta ciclos.
const maxPasosPorMensaje = 25

// Tipos de paso de la Fase 1 (conversación pura). Los de negocio van por
// Callback al pod (ia, correo, reporte, humano, pedido) o directo (webhook).
const (
	TipoMensaje   = "mensaje"
	TipoBotones   = "botones"
	TipoLista     = "lista"
	TipoEspera    = "espera"
	TipoCondicion = "condicion"
)

var claveOK = regexp.MustCompile(`^[a-z0-9_]{1,60}$`)
var numeroOK = regexp.MustCompile(`^[+\d][\d\s.\-()]{2,30}$`)

// ── Esquema de la definición (Steps en JSON) ────────────────────────────────

type Opcion struct {
	Id       string `json:"id"`
	Etiqueta string `json:"etiqueta"`
	IrA      string `json:"ir_a"`
}

type Fila struct {
	Id          string `json:"id"`
	Titulo      string `json:"titulo"`
	Descripcion string `json:"descripcion"`
	IrA         string `json:"ir_a"`
}

type Seccion struct {
	Titulo string `json:"titulo"`
	Filas  []Fila `json:"filas"`
}

type Regla struct {
	Variable string `json:"variable"`
	Es       string `json:"es"`
	IrA      string `json:"ir_a"`
}

type Paso struct {
	Clave        string    `json:"clave"`
	Tipo         string    `json:"tipo"`
	Texto        string    `json:"texto"`
	Icono        string    `json:"icono"`
	Titulo       string    `json:"titulo"`
	Pie          string    `json:"pie"`
	Botones      []Opcion  `json:"botones"`
	Secciones    []Seccion `json:"secciones"`
	TextoBoton   string    `json:"texto_boton"`
	Respaldo     string    `json:"respaldo"`
	Variable     string    `json:"variable"`
	Validacion   string    `json:"validacion"`
	Reintentos   int       `json:"reintentos"`
	MensajeError string    `json:"mensaje_error"`
	Siguiente    string    `json:"siguiente"`
	// Encuesta nativa (flow_encuesta.go): de 2 a 12 opciones con destino.
	Opciones []Opcion `json:"opciones"`
	Reglas   []Regla  `json:"reglas"`
	Defecto  string   `json:"defecto"`
	// Fase 3: pasos de negocio.
	PromptSistema  string `json:"prompt_sistema"`
	Prompt         string `json:"prompt"`
	VariableSalida string `json:"variable_salida"`
	MaxTokens      int    `json:"max_tokens"`
	Para           string `json:"para"`
	Asunto         string `json:"asunto"`
	Cuerpo         string `json:"cuerpo"`
	ReporteID      string `json:"reporte_id"`
	Formato        string `json:"formato"`
	URL            string `json:"url"`
	Metodo         string `json:"metodo"`
	Secreto        string `json:"secreto"`
	TimeoutSegs    int    `json:"timeout_segs"`
	Mensaje        string `json:"mensaje"`
	// Menús por flujo (flow_menu.go): consulta al pod y su dato.
	Accion string `json:"accion"`
	Dato   string `json:"dato"`
}

type Definicion struct {
	Inicio string `json:"inicio"`
	Pasos  []Paso `json:"pasos"`
}

// ── Envío: el motor no conoce send_service (evita ciclo de imports) ─────────
// Lo implementa pkg/sendMessage/service (FlowSender) con /send/text|button|list.

type BotonDTO struct {
	Tipo     string
	Etiqueta string
	ID       string
}

type FilaDTO struct {
	Titulo      string
	Descripcion string
	ID          string
}

type SeccionDTO struct {
	Titulo string
	Filas  []FilaDTO
}

type Sender interface {
	Texto(inst *instance_model.Instance, numero, texto string) error
	Botones(inst *instance_model.Instance, numero, titulo, descripcion, pie string, botones []BotonDTO) error
	Lista(inst *instance_model.Instance, numero, titulo, descripcion, textoBoton, pie string, secciones []SeccionDTO) error
}

// ── Servicio ────────────────────────────────────────────────────────────────

type FlowService interface {
	// Evaluar procesa un entrante. Devuelve true si un flujo lo consumió
	// (respondió él). Los errores se registran fuera: nunca rompen el webhook.
	Evaluar(ctx context.Context, inst *instance_model.Instance, remitente, texto, botonID string) (bool, error)
	ValidarDefinicion(def Definicion) error
	VistaPrevia(def Definicion) ([]map[string]string, error)
	// Enviar dispara el flujo a una lista (encuesta saliente). Devuelve el
	// conteo por resultado. Los vetados y los que ya tienen run no reciben
	// nada; los inválidos ni se intentan.
	Enviar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, remitentes []string, pausa time.Duration) map[string]int
	// Programar es Enviar sin esperar: prepara (permiso, duplicados) en el
	// acto y despacha en segundo plano (flow_envio.go).
	Programar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, remitentes []string, pausa time.Duration) (map[string]int, []DetalleEnvio)
	// Votar entrega el voto descifrado de una encuesta (flow_encuesta.go).
	Votar(ctx context.Context, inst *instance_model.Instance, remitente, encuestaID string, hashes [][]byte) (bool, error)
	SetTTL(d time.Duration)
	SetLog(log Logger)
}

// Logger es la bitácora del motor: disparo, pasos, cierres y vetos. Sin
// logger, el motor calla (las pruebas no lo necesitan).
type Logger func(formato string, args ...any)

type flowService struct {
	repo   flow_repository.FlowRepository
	sender Sender
	cb     Callback
	ttl    time.Duration
	log    Logger
	// Números con un envío saliente en curso (flow_envio.go).
	muEnvio sync.Mutex
	enEnvio map[string]bool
}

func NewFlowService(repo flow_repository.FlowRepository, sender Sender, cb Callback) FlowService {
	return &flowService{repo: repo, sender: sender, cb: cb, ttl: TTLRun}
}

func (s *flowService) SetLog(log Logger) {
	s.log = log
}

func (s *flowService) bitacora(formato string, args ...any) {
	if s.log != nil {
		s.log(formato, args...)
	}
}

func (s *flowService) SetTTL(d time.Duration) {
	if d > 0 {
		s.ttl = d
	}
}

// tocarRun sella el movimiento del run. Gorm lo hace solo en producción;
// aquí queda explícito para que cualquier repositorio (incluido el falso de
// las pruebas) vea la hora real del último movimiento y el TTL no abandone
// un run recién creado.
func tocarRun(run *flow_model.FlowRun) {
	ahora := time.Now()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = ahora
	}
	run.UpdatedAt = ahora
}

// ── Entrada ─────────────────────────────────────────────────────────────────

func normalizar(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// esBaja detecta el pedido de baja en texto libre (Ley 29733): el cliente
// que lo escribe deja de recibir flujos y se marca en el pod.
func esBaja(texto string) bool {
	switch normalizar(texto) {
	case "baja", "stop", "salir", "eliminarme", "quitarme", "borrame", "bórrame",
		"no quiero", "no mas", "no más", "no me escriban", "no me escribas",
		"darme de baja", "dame de baja", "removerme":
		return true
	}
	return false
}

func (s *flowService) Evaluar(ctx context.Context, inst *instance_model.Instance, remitente, texto, botonID string) (bool, error) {
	if inst == nil || remitente == "" || (normalizar(texto) == "" && botonID == "") {
		return false, nil
	}

	run, err := s.repo.RunActivo(ctx, inst.Id, remitente)
	if err == nil && run != nil {
		if s.vencido(ctx, inst, run) {
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
			s.bitacora("run %s vencido (%s), se abandona", run.Id, remitente)
			run = nil
		}
	}

	if run == nil {
		return s.disparar(ctx, inst, remitente, normalizar(texto))
	}
	return s.continuar(ctx, inst, remitente, run, normalizar(texto), botonID)
}

func (s *flowService) disparar(ctx context.Context, inst *instance_model.Instance, remitente, texto string) (bool, error) {
	defs, err := s.repo.DefsPorInstancia(ctx, inst.Id)
	if err != nil {
		return false, err
	}
	for i := range defs {
		d := &defs[i]
		if d.Estado != flow_model.EstadoActivo || normalizar(d.Entrada) == "" {
			continue
		}
		if normalizar(d.Entrada) != texto {
			continue
		}
		// Ley 29733: la lista de bajas vive en el pod. Sin callback no hay
		// lista que consultar y se permite (el flujo a clientes exige pod).
		// Un «no» es veto; un error no crea run (el mensaje sigue al pod) y
		// deja huella en el error para el log del hook.
		if s.cb != nil {
			resp, err := s.cb(ctx, "permiso", map[string]any{"instancia": inst.Id, "remitente": remitente})
			if err != nil {
				s.bitacora("permiso sin respuesta para %s: %v", remitente, err)
				return false, err
			}
			if ok, _ := resp["ok"].(bool); !ok {
				s.bitacora("veto de baja para %s en flujo %s", remitente, d.Id)
				return false, nil
			}
		}
		var def Definicion
		if err := json.Unmarshal(d.Steps, &def); err != nil {
			continue // definición corrupta: no se consume, sigue el curso normal
		}
		consumido, err := s.iniciar(ctx, inst, d, &def, remitente)
		if err != nil || !consumido {
			return consumido, err
		}
		return true, nil
	}
	return false, nil
}

// iniciar crea el run en el paso inicial y lo ejecuta. Devuelve false sin
// error cuando hay veto de baja. La usan el entrante y el envío saliente.
func (s *flowService) iniciar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, def *Definicion, remitente string) (bool, error) {
	run := &flow_model.FlowRun{
		FlowID: d.Id, InstanceID: inst.Id, Remitente: remitente,
		StepActual: def.Inicio, Contexto: json.RawMessage(`{}`), Estado: flow_model.RunEnCurso,
	}
	tocarRun(run)
	if err := s.repo.CrearRun(ctx, run); err != nil {
		return false, err
	}
	s.bitacora("disparo flujo %s (%s) para %s", d.Id, d.Nombre, remitente)
	if err := s.ejecutarDesde(ctx, inst, d, def, run, "", ""); err != nil {
		return true, err
	}
	return true, nil
}

var reNoDigitos = regexp.MustCompile(`[^0-9]+`)

// numeroLimpio deja solo dígitos y exige de 8 a 15 (E.164 sin +).
func numeroLimpio(numero string) string {
	d := reNoDigitos.ReplaceAllString(numero, "")
	if len(d) < 8 || len(d) > 15 {
		return ""
	}
	return d
}

// Enviar es el envío saliente síncrono (preparar + despachar en el mismo
// hilo). Lo usan las pruebas; el handler usa Programar.
func (s *flowService) Enviar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, remitentes []string, pausa time.Duration) map[string]int {
	var def Definicion
	if err := json.Unmarshal(d.Steps, &def); err != nil {
		cuenta := nuevaCuenta()
		cuenta["fallos"] = len(remitentes)
		return cuenta
	}
	aptos, cuenta, _ := s.preparar(ctx, inst, d, &def, remitentes)
	s.despachar(ctx, inst, d, &def, aptos, pausa, cuenta)
	return cuenta
}

func (s *flowService) continuar(ctx context.Context, inst *instance_model.Instance, remitente string, run *flow_model.FlowRun, texto, botonID string) (bool, error) {
	defs, err := s.repo.DefsPorInstancia(ctx, inst.Id)
	if err != nil {
		return false, err
	}
	var rec *flow_model.FlowDef
	for i := range defs {
		if defs[i].Id == run.FlowID && defs[i].Estado == flow_model.EstadoActivo {
			rec = &defs[i]
			break
		}
	}
	if rec == nil {
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		return false, nil
	}
	var def Definicion
	if err := json.Unmarshal(rec.Steps, &def); err != nil {
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		return false, nil
	}
	paso := buscarPaso(&def, run.StepActual)
	if paso == nil {
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		return false, nil
	}
	// Baja en mitad del flujo: se marca en el pod, se confirma y se cierra.
	// Solo texto libre: un botón es una respuesta al flujo, no una baja.
	if texto != "" && esBaja(texto) {
		if s.cb != nil {
			_, _ = s.cb(ctx, "baja", map[string]any{"instancia": inst.Id, "remitente": remitente})
		}
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		s.bitacora("baja atendida para %s en flujo %s", remitente, run.FlowID)
		if err := s.sender.Texto(inst, remitente, "Listo, no te escribiremos más por este medio. Si cambias de idea, escríbenos."); err != nil {
			return true, err
		}
		return true, nil
	}
	ctxVars := mapaContexto(run.Contexto)
	// Menús por flujo y consultas al pod (flow_menu.go).
	if hecho, err := s.continuarMenu(ctx, inst, rec, &def, run, paso, texto, botonID); hecho {
		return true, err
	}

	switch paso.Tipo {
	case TipoBotones, TipoLista:
		id := botonID
		if id == "" {
			visto := pasoExpandido(paso, ctxVars)
			id = opcionPorEtiqueta(&visto, texto)
		}
		sig := ""
		for _, o := range opcionesDe(paso) {
			if o.Id == id {
				sig = o.IrA
				break
			}
		}
		if sig != "" {
			// La elección queda en el contexto: es el dato de la encuesta.
			ctxVars["opcion_"+paso.Clave] = id
			run.Contexto = guardarContexto(ctxVars)
		}
		if sig == "" {
			// No eligió una alternativa válida: se repite el paso tal cual.
			if err := s.enviarPaso(inst, remitente, paso, ctxVars); err != nil {
				return true, err
			}
			tocarRun(run)
			return true, s.repo.GuardarRun(ctx, run)
		}
		run.StepActual = sig
		return true, s.ejecutarDesde(ctx, inst, rec, &def, run, "", "")
	case TipoEncuesta:
		return s.continuarEncuesta(ctx, inst, rec, &def, run, paso, texto)
	case TipoEspera:
		ok, _ := validarRespuesta(pasoValidacion(paso), texto)
		if !ok {
			n := intentos(ctxVars, paso.Variable) + 1
			ctxVars["reintento_"+paso.Variable] = fmt.Sprint(n)
			run.Contexto = guardarContexto(ctxVars)
			max := paso.Reintentos
			if max <= 0 {
				max = 2
			}
			if n > max {
				_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
				msg := plantilla(paso.MensajeError, ctxVars)
				if msg == "" {
					msg = "Lo dejamos aquí. Escríbeme de nuevo cuando quieras retomar."
				}
				return true, s.sender.Texto(inst, remitente, msg)
			}
			msg := plantilla(paso.MensajeError, ctxVars)
			if msg == "" {
				msg = "No te entendí. ¿Me lo dices de nuevo?"
			}
			if err := s.sender.Texto(inst, remitente, msg); err != nil {
				return true, err
			}
			tocarRun(run)
			return true, s.repo.GuardarRun(ctx, run)
		}
		if paso.Variable != "" {
			ctxVars[paso.Variable] = texto
		}
		run.Contexto = guardarContexto(ctxVars)
		run.StepActual = paso.Siguiente
		if paso.Siguiente == "" {
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunCompletado)
			return true, nil
		}
		return true, s.ejecutarDesde(ctx, inst, rec, &def, run, "", "")
	default:
		// El run quedó en un paso que no espera nada (mensaje/condición):
		// se reanuda desde ahí en vez de trabarse.
		return true, s.ejecutarDesde(ctx, inst, rec, &def, run, "", "")
	}
}

// ejecutarDesde avanza enviando pasos hasta uno que espera respuesta, una
// condición sin salida, o el fin (run completado).
func (s *flowService) ejecutarDesde(ctx context.Context, inst *instance_model.Instance, rec *flow_model.FlowDef, def *Definicion, run *flow_model.FlowRun, _, _ string) error {
	for i := 0; i < maxPasosPorMensaje; i++ {
		paso := buscarPaso(def, run.StepActual)
		if paso == nil {
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
			return errors.New("flujo: paso inexistente " + run.StepActual)
		}
		switch paso.Tipo {
		case TipoCondicion:
			sig := paso.Defecto
			ctxVars := mapaContexto(run.Contexto)
			for _, r := range paso.Reglas {
				if ctxVars[r.Variable] == r.Es {
					sig = r.IrA
					break
				}
			}
			if sig == "" {
				_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunCompletado)
				s.bitacora("fin flujo %s para %s (condición sin salida)", run.FlowID, run.Remitente)
				return nil
			}
			run.StepActual = sig
			continue
		case TipoEncuesta:
			if err := s.enviarEncuesta(inst, run, paso); err != nil {
				return err
			}
			tocarRun(run)
			return s.repo.GuardarRun(ctx, run)
		case TipoMensaje, TipoBotones, TipoLista, TipoEspera:
			if err := s.enviarPaso(inst, run.Remitente, paso, mapaContexto(run.Contexto)); err != nil {
				return err
			}
			if paso.Tipo == TipoMensaje {
				if paso.Siguiente == "" {
					_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunCompletado)
					s.bitacora("fin flujo %s para %s", run.FlowID, run.Remitente)
					return nil
				}
				run.StepActual = paso.Siguiente
				continue
			}
			tocarRun(run)
			return s.repo.GuardarRun(ctx, run)
		case TipoMenu:
			if err := s.enviarMenu(inst, run.Remitente, paso, mapaContexto(run.Contexto)); err != nil {
				return err
			}
			tocarRun(run)
			return s.repo.GuardarRun(ctx, run)
		case TipoConsulta:
			seguir, err := s.pasoConsulta(ctx, inst, def, run, paso, "", true)
			if err != nil || !seguir {
				return err
			}
			continue
		case TipoIA, TipoCorreo, TipoReporte, TipoWebhook, TipoHumano, TipoPedido:
			if err := s.ejecutarNegocio(ctx, inst, run, paso); err != nil {
				return err
			}
			if run.Estado != flow_model.RunEnCurso {
				return nil
			}
			continue
		default:
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
			return fmt.Errorf("flujo: tipo de paso no soportado %q", paso.Tipo)
		}
	}
	_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
	return errors.New("flujo: demasiados pasos encadenados, posible ciclo")
}

// enviarPaso manda un paso con sus {{variables}} ya expandidas (ver
// flow_plantilla.go).
func (s *flowService) enviarPaso(inst *instance_model.Instance, remitente string, original *Paso, vars map[string]string) error {
	visto := pasoExpandido(original, vars)
	paso := &visto
	texto := paso.Texto
	if paso.Icono != "" && !strings.HasPrefix(texto, paso.Icono) {
		texto = paso.Icono + " " + texto
	}
	switch paso.Tipo {
	case TipoMensaje, TipoEspera:
		return s.sender.Texto(inst, remitente, texto)
	case TipoBotones:
		botones := make([]BotonDTO, 0, len(paso.Botones))
		for _, o := range paso.Botones {
			botones = append(botones, BotonDTO{Tipo: "reply", Etiqueta: o.Etiqueta, ID: o.Id})
		}
		return s.sender.Botones(inst, remitente, paso.Titulo, texto, paso.Pie, botones)
	case TipoLista:
		secciones := make([]SeccionDTO, 0, len(paso.Secciones))
		for _, sec := range paso.Secciones {
			filas := make([]FilaDTO, 0, len(sec.Filas))
			for _, f := range sec.Filas {
				filas = append(filas, FilaDTO{Titulo: f.Titulo, Descripcion: f.Descripcion, ID: f.Id})
			}
			secciones = append(secciones, SeccionDTO{Titulo: sec.Titulo, Filas: filas})
		}
		return s.sender.Lista(inst, remitente, paso.Titulo, texto, paso.TextoBoton, paso.Pie, secciones)
	default:
		return fmt.Errorf("flujo: no se puede enviar paso %q", paso.Tipo)
	}
}

// ── Resolución de respuestas ────────────────────────────────────────────────

type opcionRef struct {
	Id  string
	IrA string
}

func opcionesDe(paso *Paso) []opcionRef {
	if paso.Tipo == TipoLista {
		var out []opcionRef
		for _, sec := range paso.Secciones {
			for _, f := range sec.Filas {
				out = append(out, opcionRef{Id: f.Id, IrA: f.IrA})
			}
		}
		return out
	}
	var out []opcionRef
	for _, o := range paso.Botones {
		out = append(out, opcionRef{Id: o.Id, IrA: o.IrA})
	}
	return out
}

func opcionPorEtiqueta(paso *Paso, texto string) string {
	t := normalizar(texto)
	if paso.Tipo == TipoLista {
		for _, sec := range paso.Secciones {
			for _, f := range sec.Filas {
				if normalizar(f.Titulo) == t {
					return f.Id
				}
			}
		}
		return ""
	}
	for _, o := range paso.Botones {
		if normalizar(o.Etiqueta) == t {
			return o.Id
		}
	}
	return ""
}

func buscarPaso(def *Definicion, clave string) *Paso {
	for i := range def.Pasos {
		if def.Pasos[i].Clave == clave {
			return &def.Pasos[i]
		}
	}
	return nil
}

func pasoValidacion(paso *Paso) string {
	if paso.Validacion == "" {
		return "texto"
	}
	return paso.Validacion
}

func validarRespuesta(validacion, texto string) (bool, string) {
	t := strings.TrimSpace(texto)
	switch validacion {
	case "numero":
		return numeroOK.MatchString(t), t
	case "fecha":
		for _, layout := range []string{"02/01/2006", "02-01-2006", "2006-01-02", "02/01/06", "2/1/2006"} {
			if _, err := time.Parse(layout, t); err == nil {
				return true, t
			}
		}
		return false, t
	case "si_no":
		switch normalizar(t) {
		case "si", "sí", "s", "no", "n":
			return true, t
		}
		return false, t
	default:
		return t != "", t
	}
}

func mapaContexto(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func guardarContexto(vars map[string]string) json.RawMessage {
	b, _ := json.Marshal(vars)
	if len(b) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}

func intentos(vars map[string]string, variable string) int {
	n := 0
	_, _ = fmt.Sscanf(vars["reintento_"+variable], "%d", &n)
	return n
}

// ── Validación de definiciones (puerta del CRUD y del activar) ──────────────

func (s *flowService) ValidarDefinicion(def Definicion) error {
	if strings.TrimSpace(def.Inicio) == "" {
		return errors.New("flujo: falta el paso inicial")
	}
	if len(def.Pasos) == 0 {
		return errors.New("flujo: sin pasos")
	}
	porClave := map[string]*Paso{}
	for i := range def.Pasos {
		p := &def.Pasos[i]
		if !claveOK.MatchString(p.Clave) {
			return fmt.Errorf("flujo: clave inválida %q (minúsculas, números, _)", p.Clave)
		}
		if _, dup := porClave[p.Clave]; dup {
			return fmt.Errorf("flujo: clave duplicada %q", p.Clave)
		}
		porClave[p.Clave] = p
	}
	if _, ok := porClave[def.Inicio]; !ok {
		return fmt.Errorf("flujo: el inicio %q no existe", def.Inicio)
	}
	irA := func(clave, donde string) error {
		if clave == "" {
			return nil
		}
		if _, ok := porClave[clave]; !ok {
			return fmt.Errorf("flujo: %s apunta a paso inexistente %q", donde, clave)
		}
		return nil
	}
	// Topes de WhatsApp (caracteres): lo que los pasa no se pinta entero en
	// el teléfono. Se miden en runas, no en bytes (el emoji pesa 4).
	cabe := func(s string, max int) bool { return len([]rune(strings.TrimSpace(s))) <= max }
	textoCabe := func(p *Paso, donde string) error {
		if !cabe(p.Texto, 1000) {
			return fmt.Errorf("flujo: %s con texto de más de 1000 caracteres", donde)
		}
		if !cabe(p.Titulo, 60) || !cabe(p.Pie, 60) {
			return fmt.Errorf("flujo: %s con título o pie de más de 60 caracteres", donde)
		}
		return nil
	}
	for i := range def.Pasos {
		p := &def.Pasos[i]
		donde := "paso " + p.Clave
		switch p.Tipo {
		case TipoMensaje:
			if strings.TrimSpace(p.Texto) == "" {
				return fmt.Errorf("flujo: %s sin texto", donde)
			}
			if err := textoCabe(p, donde); err != nil {
				return err
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoBotones:
			if strings.TrimSpace(p.Texto) == "" {
				return fmt.Errorf("flujo: %s sin texto", donde)
			}
			if err := textoCabe(p, donde); err != nil {
				return err
			}
			if len(p.Botones) < 1 || len(p.Botones) > 3 {
				return fmt.Errorf("flujo: %s lleva de 1 a 3 botones", donde)
			}
			if strings.TrimSpace(p.Respaldo) == "" {
				return fmt.Errorf("flujo: %s sin respaldo en texto (obligatorio)", donde)
			}
			for _, o := range p.Botones {
				if strings.TrimSpace(o.Id) == "" || strings.TrimSpace(o.Etiqueta) == "" {
					return fmt.Errorf("flujo: %s tiene botón sin id o etiqueta", donde)
				}
				if !cabe(o.Etiqueta, 25) {
					return fmt.Errorf("flujo: botón %q de %s con etiqueta de más de 25 caracteres", o.Id, donde)
				}
				if o.IrA == "" {
					return fmt.Errorf("flujo: botón %q de %s sin destino", o.Id, donde)
				}
				if err := irA(o.IrA, donde); err != nil {
					return err
				}
			}
		case TipoLista:
			if strings.TrimSpace(p.Texto) == "" {
				return fmt.Errorf("flujo: %s sin texto", donde)
			}
			if err := textoCabe(p, donde); err != nil {
				return err
			}
			if !cabe(p.TextoBoton, 25) {
				return fmt.Errorf("flujo: %s con texto de botón de más de 25 caracteres", donde)
			}
			if len(p.Secciones) == 0 {
				return fmt.Errorf("flujo: %s sin secciones", donde)
			}
			if strings.TrimSpace(p.Respaldo) == "" {
				return fmt.Errorf("flujo: %s sin respaldo en texto (obligatorio)", donde)
			}
			n := 0
			for _, sec := range p.Secciones {
				if !cabe(sec.Titulo, 60) {
					return fmt.Errorf("flujo: %s con sección de más de 60 caracteres", donde)
				}
				if len(sec.Filas) == 0 {
					return fmt.Errorf("flujo: %s tiene sección sin filas", donde)
				}
				for _, f := range sec.Filas {
					n++
					if strings.TrimSpace(f.Id) == "" || strings.TrimSpace(f.Titulo) == "" {
						return fmt.Errorf("flujo: %s tiene fila sin id o título", donde)
					}
					if !cabe(f.Titulo, 60) {
						return fmt.Errorf("flujo: fila %q de %s con título de más de 60 caracteres", f.Id, donde)
					}
					if !cabe(f.Descripcion, 150) {
						return fmt.Errorf("flujo: fila %q de %s con descripción de más de 150 caracteres", f.Id, donde)
					}
					if f.IrA == "" {
						return fmt.Errorf("flujo: fila %q de %s sin destino", f.Id, donde)
					}
					if err := irA(f.IrA, donde); err != nil {
						return err
					}
				}
			}
			if n == 0 {
				return fmt.Errorf("flujo: %s sin filas", donde)
			}
			if n > 10 {
				return fmt.Errorf("flujo: %s con más de 10 filas (tope de la lista)", donde)
			}
		case TipoEspera:
			if !claveOK.MatchString(p.Variable) {
				return fmt.Errorf("flujo: %s sin variable válida donde guardar", donde)
			}
			switch pasoValidacion(p) {
			case "texto", "numero", "fecha", "si_no":
			default:
				return fmt.Errorf("flujo: %s con validación desconocida %q", donde, p.Validacion)
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoCondicion:
			if len(p.Reglas) == 0 {
				return fmt.Errorf("flujo: %s sin reglas", donde)
			}
			for _, r := range p.Reglas {
				if strings.TrimSpace(r.Variable) == "" || r.IrA == "" {
					return fmt.Errorf("flujo: %s tiene regla incompleta", donde)
				}
				if err := irA(r.IrA, donde); err != nil {
					return err
				}
			}
			if err := irA(p.Defecto, donde); err != nil {
				return err
			}
		case TipoIA:
			if strings.TrimSpace(p.Prompt) == "" {
				return fmt.Errorf("flujo: %s sin pregunta para la IA", donde)
			}
			if p.VariableSalida != "" && !claveOK.MatchString(p.VariableSalida) {
				return fmt.Errorf("flujo: %s con variable de salida inválida", donde)
			}
			if p.MaxTokens != 0 && (p.MaxTokens < 32 || p.MaxTokens > 4000) {
				return fmt.Errorf("flujo: %s con max_tokens fuera de 32-4000", donde)
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoCorreo:
			if strings.TrimSpace(p.Para) == "" {
				return fmt.Errorf("flujo: %s sin destinatario", donde)
			}
			if !strings.Contains(p.Para, "@") && !strings.Contains(p.Para, "{{") {
				return fmt.Errorf("flujo: %s con destinatario inválido", donde)
			}
			if strings.TrimSpace(p.Asunto) == "" || strings.TrimSpace(p.Cuerpo) == "" {
				return fmt.Errorf("flujo: %s sin asunto o cuerpo", donde)
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoReporte:
			if !reportesConocidos[p.ReporteID] {
				return fmt.Errorf("flujo: %s con reporte desconocido %q", donde, p.ReporteID)
			}
			if p.Formato != "" && p.Formato != "texto" && p.Formato != "csv" && p.Formato != "pdf" {
				return fmt.Errorf("flujo: %s con formato desconocido %q", donde, p.Formato)
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoWebhook:
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.URL)), "http://") &&
				!strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.URL)), "https://") {
				return fmt.Errorf("flujo: %s sin URL http(s)", donde)
			}
			if m := strings.ToUpper(strings.TrimSpace(p.Metodo)); m != "" && m != "GET" && m != "POST" {
				return fmt.Errorf("flujo: %s con método distinto de GET/POST", donde)
			}
			if p.TimeoutSegs != 0 && (p.TimeoutSegs < 2 || p.TimeoutSegs > 30) {
				return fmt.Errorf("flujo: %s con timeout fuera de 2-30 s", donde)
			}
			if err := irA(p.Siguiente, donde); err != nil {
				return err
			}
		case TipoEncuesta:
			if err := validarEncuesta(p, donde, irA); err != nil {
				return err
			}
		case TipoConsulta, TipoMenu:
			if err := validarPasoMenu(p, donde, irA); err != nil {
				return err
			}
		case TipoHumano, TipoPedido:
			// Sin campos obligatorios: el mensaje es opcional (hay puente por defecto).
		default:
			return fmt.Errorf("flujo: %s con tipo desconocido %q", donde, p.Tipo)
		}
	}
	return validarDefectos(def, porClave)
}

// VistaPrevia valida y devuelve los pasos en orden de definición, sin enviar
// nada. Es lo que usa POST /flow/probar.
func (s *flowService) VistaPrevia(def Definicion) ([]map[string]string, error) {
	if err := s.ValidarDefinicion(def); err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(def.Pasos))
	for _, p := range def.Pasos {
		resumen := p.Texto
		if p.Tipo == TipoBotones {
			etiquetas := make([]string, 0, len(p.Botones))
			for _, o := range p.Botones {
				etiquetas = append(etiquetas, o.Etiqueta)
			}
			resumen = p.Texto + " [" + strings.Join(etiquetas, " | ") + "]"
		}
		if p.Tipo == TipoLista {
			n := 0
			for _, sec := range p.Secciones {
				n += len(sec.Filas)
			}
			resumen = fmt.Sprintf("%s [%d opciones en lista]", p.Texto, n)
		}
		switch p.Tipo {
		case TipoEncuesta:
			resumen = resumenEncuesta(&p)
		case TipoConsulta, TipoMenu:
			resumen = resumenMenu(&p)
		case TipoIA:
			resumen = "IA: " + recortar(p.Prompt, 80)
		case TipoCorreo:
			resumen = fmt.Sprintf("Correo a %s: %s", p.Para, p.Asunto)
		case TipoReporte:
			resumen = fmt.Sprintf("Reporte %s (%s)", p.ReporteID, formatoReporte(&p))
		case TipoWebhook:
			resumen = fmt.Sprintf("Webhook %s %s", metodoWebhook(&p), p.URL)
		case TipoHumano:
			resumen = "Deriva a una persona"
		case TipoPedido:
			resumen = "Deriva a ventas (pedido)"
		}
		out = append(out, map[string]string{"clave": p.Clave, "tipo": p.Tipo, "resumen": resumen})
	}
	return out, nil
}
