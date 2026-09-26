package flow_service

// ORQUESTADOR: una sola decisión por mensaje entrante, en un solo lugar.
//
// Antes, el motor de flujos (aquí) y el menú del pod contestaban cada uno por
// su lado: con los dos encendidos un mismo «hola» recibía dos respuestas. Ahora
// evolution-go decide ANTES de mandar el webhook y lo anota en él
// (`pjgFlujo`): si atiende un flujo, el pod guarda el mensaje en su bandeja y
// bitácora pero NO responde; si no, el pod sigue como siempre, salvo que el
// menú clásico de una audiencia cuyo flujo por defecto está activo calla.
//
// Orden (el primero que aplica gana):
//
//	1. El pod dice que una PERSONA lleva la conversación → nadie del bot
//	   (el run en curso, si lo hay, se abandona).
//	2. El pod reserva el mensaje para uno de sus pasos previos al menú (baja,
//	   «ok 4» del titular, cita «1/2», reparto, fuera de horario, pedido en
//	   manos del bot de ventas, número descartado por las reglas) → pod.
//	   Sobre un run de un flujo normal solo manda la baja: el flujo sigue.
//	3. Run en curso → su flujo.
//	4. Entrada exacta de un flujo activo → ese flujo (con permiso Ley 29733).
//	   La entrada de un flujo por defecto solo vale para su audiencia.
//	5. Flujo por defecto activo de la AUDIENCIA (gerencia si el número es el
//	   titular o un autorizado; cliente en otro caso) → ese flujo.
//	6. Nada → pod, como hoy.
//
// Tomada, reserva y audiencia las sabe el pod: callback «rol» con 1,5 s de
// tope, caché de 10 s por remitente y pausa de 15 s tras un fallo. Si el pod
// no contesta se degrada al comportamiento de antes (run y entrada sí, menú
// por defecto no y SIN silenciar el menú clásico): nunca silencio, nunca dos
// respuestas.

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// Orquestador lo implementa flowService. whatsmeow lo pide por aserción de
// tipo: sin él, el entrante sigue con Evaluar como antes.
type Orquestador interface {
	// Decidir es síncrono y corto (base + como mucho un callback acotado):
	// corre antes del webhook. Nunca envía mensajes.
	Decidir(ctx context.Context, inst *instance_model.Instance, remitente, texto, botonID string) Decision
	// Atender ejecuta lo decidido (envía). Va en segundo plano.
	Atender(ctx context.Context, d Decision) error
}

var _ Orquestador = (*flowService)(nil)

// Motivos de la decisión (van en la marca del webhook y en el log).
const (
	MotivoRun          = "run"
	MotivoEntrada      = "entrada"
	MotivoDefecto      = "defecto"
	MotivoTomada       = "tomada"
	MotivoSinFlujo     = "sin_flujo"
	MotivoSinTexto     = "sin_texto"
	MotivoPodCaido     = "pod_sin_respuesta"
	MotivoSinPermiso   = "sin_permiso"
	MotivoError        = "error"
	motivoReservadoPre = "reservado:"
)

// Decision es lo que el orquestador resolvió para un mensaje.
type Decision struct {
	Atiende   bool
	Motivo    string
	FlowID    string
	Audiencia string
	// Menus: audiencias con flujo por defecto activo. El pod calla su menú
	// clásico para ellas. Solo se anuncian cuando la decisión es fiable.
	Menus []string

	marcar    bool
	inst      *instance_model.Instance
	remitente string
	texto     string
	botonID   string
	def       *flow_model.FlowDef
}

// Marca es lo que viaja en el webhook como `pjgFlujo`. nil = nada que decir
// (el pod hace lo de siempre).
func (d Decision) Marca() map[string]any {
	if d.Atiende {
		return map[string]any{"atendido": true, "motivo": d.Motivo, "flow_id": d.FlowID, "audiencia": d.Audiencia}
	}
	if !d.marcar || len(d.Menus) == 0 {
		return nil
	}
	return map[string]any{"atendido": false, "motivo": d.Motivo, "menus_en_flujo": d.Menus}
}

// ── Callback «rol» con caché y pausa tras fallo ─────────────────────────────

const (
	rolTimeout        = 1500 * time.Millisecond
	rolCacheTTL       = 10 * time.Second
	rolPausaTrasFallo = 15 * time.Second
)

type rolPod struct {
	Audiencia string
	Tomada    bool
	Permiso   bool
	Reservado bool
	Motivo    string
}

type entradaRol struct {
	texto string
	rol   rolPod
	vence time.Time
}

var estadoRol = struct {
	sync.Mutex
	cache map[string]entradaRol
	pausa map[string]time.Time
}{cache: map[string]entradaRol{}, pausa: map[string]time.Time{}}

func claveRol(inst, remitente string) string { return inst + "|" + remitente }

// olvidarRol tira lo sabido de un remitente: un paso de flujo pudo cambiar su
// estado en el pod (derivado a una persona, pedido al bot de ventas).
func olvidarRol(inst, remitente string) {
	estadoRol.Lock()
	delete(estadoRol.cache, claveRol(inst, remitente))
	estadoRol.Unlock()
}

// reiniciarRol vacía caché y pausas (pruebas).
func reiniciarRol() {
	estadoRol.Lock()
	estadoRol.cache = map[string]entradaRol{}
	estadoRol.pausa = map[string]time.Time{}
	estadoRol.Unlock()
}

func aBool(v any) bool { b, _ := v.(bool); return b }

func (s *flowService) rolDe(ctx context.Context, inst *instance_model.Instance, remitente, texto string) (rolPod, bool) {
	if s.cb == nil {
		return rolPod{}, false
	}
	clave := claveRol(inst.Id, remitente)
	t := normalizar(texto)
	ahora := time.Now()
	estadoRol.Lock()
	if e, ok := estadoRol.cache[clave]; ok && e.texto == t && ahora.Before(e.vence) {
		estadoRol.Unlock()
		return e.rol, true
	}
	if hasta, ok := estadoRol.pausa[inst.Id]; ok && ahora.Before(hasta) {
		estadoRol.Unlock()
		return rolPod{}, false
	}
	estadoRol.Unlock()

	c, cancelar := context.WithTimeout(ctx, rolTimeout)
	defer cancelar()
	resp, err := s.cb(c, "rol", map[string]any{
		"instancia": inst.Id, "instancia_nombre": inst.Name, "remitente": remitente, "texto": texto,
	})
	if err != nil || resp == nil {
		s.bitacora("orquestador: rol sin respuesta para %s: %v (se degrada 15 s)", remitente, err)
		estadoRol.Lock()
		estadoRol.pausa[inst.Id] = time.Now().Add(rolPausaTrasFallo)
		estadoRol.Unlock()
		return rolPod{}, false
	}
	r := rolPod{
		Audiencia: flow_model.AudienciaCliente,
		Tomada:    aBool(resp["tomada"]),
		Permiso:   aBool(resp["permiso"]),
		Reservado: aBool(resp["reservado"]),
	}
	if a, _ := resp["audiencia"].(string); a == flow_model.AudienciaGerencia {
		r.Audiencia = a
	}
	r.Motivo, _ = resp["motivo"].(string)
	estadoRol.Lock()
	estadoRol.cache[clave] = entradaRol{texto: t, rol: r, vence: time.Now().Add(rolCacheTTL)}
	delete(estadoRol.pausa, inst.Id)
	estadoRol.Unlock()
	return r, true
}

// permisoDe es el veto de Ley 29733 cuando no hay rol: el mismo callback
// «permiso» de siempre, pero con el tope corto del orquestador.
func (s *flowService) permisoDe(ctx context.Context, inst *instance_model.Instance, remitente string) (bool, error) {
	if s.cb == nil {
		return true, nil
	}
	estadoRol.Lock()
	hasta, pausado := estadoRol.pausa[inst.Id]
	estadoRol.Unlock()
	if pausado && time.Now().Before(hasta) {
		return false, errPodPausado
	}
	c, cancelar := context.WithTimeout(ctx, rolTimeout)
	defer cancelar()
	resp, err := s.cb(c, "permiso", map[string]any{"instancia": inst.Id, "instancia_nombre": inst.Name, "remitente": remitente})
	if err != nil {
		return false, err
	}
	return aBool(resp["ok"]), nil
}

type errorPod string

func (e errorPod) Error() string { return string(e) }

const errPodPausado = errorPod("pod en pausa tras un fallo reciente")

// ── Decidir ─────────────────────────────────────────────────────────────────

// runVigente devuelve el run en curso si sigue vivo y su flujo activo; si
// no, lo cierra como abandonado (lo mismo que haría `continuar`).
func (s *flowService) runVigente(ctx context.Context, inst *instance_model.Instance, remitente string, defs []flow_model.FlowDef) (*flow_model.FlowRun, *flow_model.FlowDef) {
	run, err := s.repo.RunActivo(ctx, inst.Id, remitente)
	if err != nil || run == nil {
		return nil, nil
	}
	if s.vencido(ctx, inst, run) {
		_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		s.bitacora("run %s vencido (%s), se abandona", run.Id, remitente)
		return nil, nil
	}
	for i := range defs {
		if defs[i].Id == run.FlowID && defs[i].Estado == flow_model.EstadoActivo {
			return run, &defs[i]
		}
	}
	_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
	return nil, nil
}

func (s *flowService) Decidir(ctx context.Context, inst *instance_model.Instance, remitente, texto, botonID string) Decision {
	d := Decision{Motivo: MotivoSinFlujo, inst: inst, remitente: remitente, texto: texto, botonID: botonID}
	if inst == nil || remitente == "" {
		return d
	}
	defs, err := s.repo.DefsPorInstancia(ctx, inst.Id)
	if err != nil {
		d.Motivo = MotivoError
		return d
	}
	// Un flujo por defecto por audiencia: el más reciente (la lista viene
	// ordenada por updated_at DESC; activar ya pausa a los demás).
	porDefecto := map[string]*flow_model.FlowDef{}
	for i := range defs {
		f := &defs[i]
		if f.Estado != flow_model.EstadoActivo || !f.PorDefecto {
			continue
		}
		if f.Audiencia != flow_model.AudienciaCliente && f.Audiencia != flow_model.AudienciaGerencia {
			continue
		}
		if _, ya := porDefecto[f.Audiencia]; !ya {
			porDefecto[f.Audiencia] = f
		}
	}
	for a := range porDefecto {
		d.Menus = append(d.Menus, a)
	}
	sort.Strings(d.Menus)

	t := normalizar(texto)
	if t == "" && botonID == "" {
		// Adjunto, nota de voz, voto de encuesta: el motor no los atiende.
		// El pod sí (factura, audio del dueño…) pero sin su menú clásico
		// para las audiencias que ya van por flujo.
		d.Motivo = MotivoSinTexto
		d.marcar = true
		return d
	}

	run, runDef := s.runVigente(ctx, inst, remitente, defs)
	var entradas []*flow_model.FlowDef
	for i := range defs {
		f := &defs[i]
		if f.Estado == flow_model.EstadoActivo && normalizar(f.Entrada) != "" && normalizar(f.Entrada) == t {
			entradas = append(entradas, f)
		}
	}
	if run == nil && len(entradas) == 0 && len(porDefecto) == 0 {
		return d // ningún flujo puede atender: el pod, como siempre, sin marca
	}

	rol, rolOK := s.rolDe(ctx, inst, remitente, texto)
	abandonar := func() {
		if run != nil {
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
			s.bitacora("orquestador: run %s abandonado (%s) para %s", run.Id, d.Motivo, remitente)
		}
	}
	if rolOK && rol.Tomada {
		d.Motivo = MotivoTomada
		d.marcar = true
		abandonar()
		return d
	}
	if rolOK && rol.Reservado && (rol.Motivo == "baja" || run == nil || (runDef != nil && runDef.PorDefecto)) {
		d.Motivo = motivoReservadoPre + rol.Motivo
		d.marcar = true
		if rol.Motivo == "baja" {
			abandonar()
		}
		return d
	}
	atiende := func(motivo string, f *flow_model.FlowDef) Decision {
		d.Atiende, d.Motivo, d.def = true, motivo, f
		if f != nil {
			d.FlowID, d.Audiencia = f.Id, f.Audiencia
		}
		return d
	}
	if run != nil {
		return atiende(MotivoRun, runDef)
	}
	for _, f := range entradas {
		if f.PorDefecto && f.Audiencia != "" && (!rolOK || rol.Audiencia != f.Audiencia) {
			continue // la entrada de un menú solo vale para su audiencia
		}
		permitido := rol.Permiso
		if !rolOK {
			ok, err := s.permisoDe(ctx, inst, remitente)
			if err != nil {
				s.bitacora("orquestador: permiso sin respuesta para %s: %v", remitente, err)
				d.Motivo = MotivoError
				return d
			}
			permitido = ok
		}
		if !permitido {
			s.bitacora("orquestador: veto de baja para %s en flujo %s", remitente, f.Id)
			d.Motivo = MotivoSinPermiso
			return d
		}
		return atiende(MotivoEntrada, f)
	}
	if len(porDefecto) == 0 {
		return d
	}
	if !rolOK {
		// Sin saber quién escribe no se elige menú: el clásico contesta, como
		// antes de los menús por flujo.
		d.Motivo = MotivoPodCaido
		return d
	}
	f := porDefecto[rol.Audiencia]
	if f == nil {
		// Esta audiencia sigue con su menú clásico; las otras callan.
		d.Motivo = MotivoSinFlujo
		d.marcar = true
		return d
	}
	if !rol.Permiso {
		d.Motivo = MotivoSinPermiso
		return d
	}
	return atiende(MotivoDefecto, f)
}

// ── Atender ─────────────────────────────────────────────────────────────────

// Un remitente a la vez: dos mensajes seguidos no se pisan el run.
var candados sync.Map

func candadoDe(clave string) func() {
	v, _ := candados.LoadOrStore(clave, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *flowService) Atender(ctx context.Context, d Decision) error {
	if !d.Atiende || d.inst == nil {
		return nil
	}
	ctx = conTextoCrudo(ctx, d.texto)
	liberar := candadoDe(claveRol(d.inst.Id, d.remitente))
	defer liberar()
	defer olvidarRol(d.inst.Id, d.remitente)

	// Si entre decidir y atender nació un run (el mensaje anterior del mismo
	// número), este mensaje es su respuesta.
	if run, err := s.repo.RunActivo(ctx, d.inst.Id, d.remitente); err == nil && run != nil && !s.vencido(ctx, d.inst, run) {
		_, err := s.continuar(ctx, d.inst, d.remitente, run, normalizar(d.texto), d.botonID)
		return err
	}
	if d.def == nil {
		return nil
	}
	var def Definicion
	if err := json.Unmarshal(d.def.Steps, &def); err != nil {
		return err
	}
	switch d.Motivo {
	case MotivoEntrada:
		_, err := s.iniciar(ctx, d.inst, d.def, &def, d.remitente)
		return err
	case MotivoDefecto:
		return s.iniciarMenu(ctx, d.inst, d.def, &def, d.remitente, d.texto, d.botonID)
	}
	return nil
}

// ── Nombre de la instancia en los callbacks ─────────────────────────────────

// ConNombreDeInstancia añade `instancia_nombre` a cada callback. El pod
// resuelve la empresa por el NOMBRE de la instancia (su ajuste
// whatsapp.c{id}.instance); el motor solo guarda el id.
func ConNombreDeInstancia(cb Callback, nombre func(id string) string) Callback {
	if cb == nil || nombre == nil {
		return cb
	}
	var mu sync.Mutex
	vistos := map[string]string{}
	return func(ctx context.Context, accion string, payload map[string]any) (map[string]any, error) {
		if id, _ := payload["instancia"].(string); id != "" {
			if _, ya := payload["instancia_nombre"]; !ya {
				mu.Lock()
				n, ok := vistos[id]
				mu.Unlock()
				if !ok {
					n = nombre(id)
					if n != "" {
						mu.Lock()
						vistos[id] = n
						mu.Unlock()
					}
				}
				if n != "" {
					payload["instancia_nombre"] = n
				}
			}
		}
		return cb(ctx, accion, payload)
	}
}
