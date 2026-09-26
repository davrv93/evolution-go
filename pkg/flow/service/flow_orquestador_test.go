package flow_service

// Orquestador y menús por flujo. Repositorio, envío y pod falsos: nada sale
// a la red ni a WhatsApp.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// podFalso responde los callbacks como lo haría el pod.
type podFalso struct {
	mu        sync.Mutex
	caido     bool
	roles     map[string]map[string]any // remitente → respuesta de «rol»
	consultas map[string]map[string]any // accion → respuesta de «consulta»
	llamadas  []string
	datos     []string // dato de cada consulta recibida
}

func (p *podFalso) cb(_ context.Context, accion string, payload map[string]any) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.llamadas = append(p.llamadas, accion)
	if p.caido {
		return nil, errors.New("pod caído")
	}
	switch accion {
	case "rol":
		rem, _ := payload["remitente"].(string)
		if r, ok := p.roles[rem]; ok {
			return r, nil
		}
		return map[string]any{"audiencia": "cliente", "permiso": true}, nil
	case "permiso":
		return map[string]any{"ok": true}, nil
	case "consulta":
		a, _ := payload["accion"].(string)
		d, _ := payload["dato"].(string)
		p.datos = append(p.datos, a+"="+d)
		if r, ok := p.consultas[a]; ok {
			return r, nil
		}
		return map[string]any{"texto": "respuesta de " + a}, nil
	}
	return map[string]any{"ok": true}, nil
}

func (p *podFalso) cuantas(accion string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, a := range p.llamadas {
		if a == accion {
			n++
		}
	}
	return n
}

// menuClientes: el menú de clientes convertido (texto numerado).
func menuClientes() Definicion {
	return Definicion{Inicio: "menu", Pasos: []Paso{
		{Clave: "menu", Tipo: TipoMenu, Texto: "Hola 👋 ¿En qué te ayudo?", Opciones: []Opcion{
			{Id: "wa2n:1", Etiqueta: "Horario de atención", IrA: "horario"},
			{Id: "wa2n:2", Etiqueta: "Consultar stock", IrA: "stock_pide"},
			{Id: "wa2n:3", Etiqueta: "Hablar con una persona", IrA: "humano"},
		}},
		{Clave: "horario", Tipo: TipoConsulta, Accion: "horario"},
		{Clave: "stock_pide", Tipo: TipoEspera, Texto: "¿Qué producto buscas?", Variable: "producto", Validacion: "texto", Siguiente: "stock"},
		{Clave: "stock", Tipo: TipoConsulta, Accion: "stock", Dato: "{{producto}}"},
		{Clave: "humano", Tipo: TipoHumano, Mensaje: "Te paso con una persona del equipo. 🙋"},
	}}
}

// menuGerencia: lista de opciones + texto libre al asistente.
func menuGerencia() Definicion {
	return Definicion{Inicio: "menu", Pasos: []Paso{
		{Clave: "menu", Tipo: TipoLista, Texto: "🏪 Asistente de gerencia", TextoBoton: "Ver opciones", Respaldo: "1 ventas, 2 caja",
			Defecto: "asistente",
			Secciones: []Seccion{{Filas: []Fila{
				{Id: "wa2g:1", Titulo: "Ventas de hoy", IrA: "g1"},
				{Id: "wa2g:2", Titulo: "Caja", IrA: "g2"},
			}}}},
		{Clave: "g1", Tipo: TipoConsulta, Accion: "gerencia:1"},
		{Clave: "g2", Tipo: TipoConsulta, Accion: "gerencia:2"},
		{Clave: "asistente", Tipo: TipoConsulta, Accion: "gerencia"},
	}}
}

type banco struct {
	t      *testing.T
	repo   *repoFalso
	sender *senderFalso
	pod    *podFalso
	svc    *flowService
	inst   *instance_model.Instance
}

func nuevoBanco(t *testing.T) *banco {
	t.Helper()
	reiniciarRol()
	b := &banco{
		t:      t,
		repo:   &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}},
		sender: &senderFalso{},
		pod:    &podFalso{roles: map[string]map[string]any{}, consultas: map[string]map[string]any{}},
		inst:   &instance_model.Instance{Id: "inst-orq", Name: "botica"},
	}
	b.svc = NewFlowService(b.repo, b.sender, b.pod.cb).(*flowService)
	return b
}

func (b *banco) flujo(id, entrada, audiencia string, porDefecto bool, def Definicion) {
	b.t.Helper()
	if err := b.svc.ValidarDefinicion(def); err != nil {
		b.t.Fatalf("definición %s inválida: %v", id, err)
	}
	steps, _ := json.Marshal(def)
	b.repo.defs[id] = flow_model.FlowDef{Id: id, InstanceID: b.inst.Id, Nombre: id, Estado: flow_model.EstadoActivo,
		Entrada: entrada, Audiencia: audiencia, PorDefecto: porDefecto, Steps: steps}
}

// mensaje decide y, si toca, atiende. Devuelve la decisión y lo enviado.
func (b *banco) mensaje(remitente, texto, boton string) (Decision, []envioRegistrado) {
	b.t.Helper()
	antes := len(b.sender.envios)
	d := b.svc.Decidir(context.Background(), b.inst, remitente, texto, boton)
	if err := b.svc.Atender(context.Background(), d); err != nil {
		b.t.Fatalf("atender: %v", err)
	}
	return d, b.sender.envios[antes:]
}

// sinDobleRespuesta: si el flujo atiende, la marca le dice al pod que calle;
// si no atiende, el motor no envió nada.
func sinDobleRespuesta(t *testing.T, d Decision, enviados []envioRegistrado) {
	t.Helper()
	m := d.Marca()
	if d.Atiende {
		if m == nil || m["atendido"] != true {
			t.Fatalf("atiende el flujo pero la marca no calla al pod: %v", m)
		}
		return
	}
	if len(enviados) > 0 {
		t.Fatalf("el flujo envió %d mensajes sin haber decidido atender (%s)", len(enviados), d.Motivo)
	}
	if m != nil && m["atendido"] != false {
		t.Fatalf("marca incoherente: %v", m)
	}
}

func TestOrquestadorSinFlujosNoMarcaNiLlamaAlPod(t *testing.T) {
	b := nuevoBanco(t)
	d, env := b.mensaje("51999000001", "hola", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Marca() != nil {
		t.Fatalf("sin flujos debe ser el pod sin marca: %+v", d)
	}
	if len(b.pod.llamadas) != 0 {
		t.Fatalf("sin flujos no se llama al pod: %v", b.pod.llamadas)
	}
}

func TestOrquestadorMenuPorDefectoClienteYGerente(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.flujo("ger", "gerencia", flow_model.AudienciaGerencia, true, menuGerencia())
	b.pod.roles["51900000009"] = map[string]any{"audiencia": "gerencia", "permiso": true}

	d, env := b.mensaje("51999000002", "hola", "")
	sinDobleRespuesta(t, d, env)
	if !d.Atiende || d.FlowID != "cli" || d.Motivo != MotivoDefecto {
		t.Fatalf("cliente debe ir al menú de clientes: %+v", d)
	}
	if len(env) != 1 || !strings.Contains(env[0].texto, "*1.* Horario de atención") {
		t.Fatalf("debe llegar el menú numerado: %+v", env)
	}

	d, env = b.mensaje("51900000009", "hola", "")
	sinDobleRespuesta(t, d, env)
	if !d.Atiende || d.FlowID != "ger" || d.Audiencia != "gerencia" {
		t.Fatalf("el gerente debe ir al menú gerencial: %+v", d)
	}
	if len(env) != 1 || env[0].kind != "lista" {
		t.Fatalf("el gerente recibe la lista: %+v", env)
	}
}

func TestOrquestadorRunEnCursoGana(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.flujo("encuesta", "encuesta", "", false, definicionEncuesta())
	// Arranca la encuesta por su entrada: ya hay run.
	d, env := b.mensaje("51999000003", "encuesta", "")
	sinDobleRespuesta(t, d, env)
	if d.Motivo != MotivoEntrada || d.FlowID != "encuesta" {
		t.Fatalf("la entrada dispara su flujo: %+v", d)
	}
	// Siguiente mensaje: el run en curso gana sobre el menú por defecto.
	d, env = b.mensaje("51999000003", "hola", "")
	sinDobleRespuesta(t, d, env)
	if d.Motivo != MotivoRun || d.FlowID != "encuesta" {
		t.Fatalf("el run en curso debe ganar: %+v", d)
	}
}

func TestOrquestadorHumanoTomadoSilencioDelBot(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	d, _ := b.mensaje("51999000004", "hola", "")
	if !d.Atiende {
		t.Fatalf("primero el menú: %+v", d)
	}
	// Una persona toma la conversación en la bandeja del pod.
	b.pod.roles["51999000004"] = map[string]any{"audiencia": "cliente", "permiso": true, "tomada": true}
	reiniciarRol()
	d, env := b.mensaje("51999000004", "2", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != MotivoTomada || len(env) != 0 {
		t.Fatalf("tomada por una persona: el bot calla (%+v, %d envíos)", d, len(env))
	}
	m := d.Marca()
	if m == nil || m["atendido"] != false {
		t.Fatalf("el pod debe saber que el menú clásico también calla: %v", m)
	}
	if run, err := b.repo.RunActivo(context.Background(), b.inst.Id, "51999000004"); err == nil && run != nil {
		t.Fatalf("el run en curso se abandona al tomarla una persona")
	}
}

func TestOrquestadorPodCaidoComportamientoDeHoy(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.flujo("promo", "promo", "", false, definicionEncuesta())
	b.pod.caido = true

	// Menú por defecto: sin saber quién escribe no se usa, y el menú clásico
	// NO se silencia (marca nil) → contesta el pod como antes.
	d, env := b.mensaje("51999000005", "hola", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != MotivoPodCaido || d.Marca() != nil {
		t.Fatalf("pod caído: menú clásico como hoy, sin marca: %+v", d)
	}
	// La pausa evita otro callback durante 15 s.
	n := b.pod.cuantas("rol")
	b.mensaje("51999000005", "otra cosa", "")
	if b.pod.cuantas("rol") != n {
		t.Fatalf("tras el fallo se espera antes de volver a llamar al pod")
	}
	// Entrada: igual que hoy, sin permiso no se dispara (y no hay silencio:
	// el mensaje sigue al pod sin marca).
	d, env = b.mensaje("51999000006", "promo", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Marca() != nil {
		t.Fatalf("sin permiso no se dispara la entrada: %+v", d)
	}
}

func TestOrquestadorPodCaidoRunSigue(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("promo", "promo", "", false, definicionEncuesta())
	d, _ := b.mensaje("51999000007", "promo", "")
	if !d.Atiende {
		t.Fatalf("arranca: %+v", d)
	}
	b.pod.caido = true
	reiniciarRol()
	d, env := b.mensaje("51999000007", "sí", "")
	sinDobleRespuesta(t, d, env)
	if !d.Atiende || d.Motivo != MotivoRun {
		t.Fatalf("con el pod caído el run en curso sigue, como hoy: %+v", d)
	}
}

func TestOrquestadorReservadoPorElPod(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.flujo("promo", "promo", "", false, definicionEncuesta())
	// Cita pendiente: el «1» es de la agenda, aunque haya menú por flujo.
	b.pod.roles["51999000008"] = map[string]any{"audiencia": "cliente", "permiso": true, "reservado": true, "motivo": "agenda"}
	d, env := b.mensaje("51999000008", "1", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != "reservado:agenda" {
		t.Fatalf("la agenda gana: %+v", d)
	}
	// Con un run de un flujo NORMAL, la reserva no lo interrumpe (salvo baja).
	delete(b.pod.roles, "51999000008")
	reiniciarRol()
	b.mensaje("51999000008", "promo", "")
	b.pod.roles["51999000008"] = map[string]any{"audiencia": "cliente", "permiso": true, "reservado": true, "motivo": "agenda"}
	reiniciarRol()
	d, _ = b.mensaje("51999000008", "1", "")
	if !d.Atiende || d.Motivo != MotivoRun {
		t.Fatalf("el run del flujo normal sigue: %+v", d)
	}
	// La baja sí corta el run.
	b.pod.roles["51999000008"] = map[string]any{"audiencia": "cliente", "permiso": true, "reservado": true, "motivo": "baja"}
	reiniciarRol()
	d, env = b.mensaje("51999000008", "quiero la baja", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != "reservado:baja" {
		t.Fatalf("la baja la atiende el pod: %+v", d)
	}
	if run, err := b.repo.RunActivo(context.Background(), b.inst.Id, "51999000008"); err == nil && run != nil {
		t.Fatalf("la baja abandona el run")
	}
}

func TestOrquestadorEntradaDeMenuSoloParaSuAudiencia(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("ger", "gerencia", flow_model.AudienciaGerencia, true, menuGerencia())
	d, env := b.mensaje("51999000010", "gerencia", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende {
		t.Fatalf("un cliente no dispara el menú gerencial por su entrada: %+v", d)
	}
	m := d.Marca()
	if m == nil || m["atendido"] != false {
		t.Fatalf("el cliente sigue con su menú clásico; la marca lo dice: %v", m)
	}
	menus, _ := m["menus_en_flujo"].([]string)
	if len(menus) != 1 || menus[0] != "gerencia" {
		t.Fatalf("solo el menú gerencial va por flujo: %v", menus)
	}
}

func TestOrquestadorSinTextoSoloMarca(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	d, env := b.mensaje("51999000011", "", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != MotivoSinTexto || len(b.pod.llamadas) != 0 {
		t.Fatalf("un adjunto no lo atiende el motor ni pregunta al pod: %+v %v", d, b.pod.llamadas)
	}
	if m := d.Marca(); m == nil || m["atendido"] != false {
		t.Fatalf("pero el pod debe callar su menú clásico: %v", m)
	}
}

func TestOrquestadorSinPermisoNoSilencia(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.pod.roles["51999000012"] = map[string]any{"audiencia": "cliente", "permiso": false}
	d, env := b.mensaje("51999000012", "hola", "")
	sinDobleRespuesta(t, d, env)
	if d.Atiende || d.Motivo != MotivoSinPermiso || d.Marca() != nil {
		t.Fatalf("de baja: ni flujo ni silencio, el pod como hoy: %+v", d)
	}
}

// ── Menús por flujo: comportamiento del usuario final ───────────────────────

func TestMenuClientesNumeroEtiquetaYConsulta(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.pod.consultas["horario"] = map[string]any{"texto": "🕘 *Horario de atención*\nLunes: 08:00 – 20:00"}
	b.pod.consultas["stock"] = map[string]any{"texto": "• Paracetamol — 40 und."}

	b.mensaje("51999000020", "hola", "")
	// «1» elige la opción 1 (horario, consulta al pod) y cierra el run.
	_, env := b.mensaje("51999000020", "1", "")
	if len(env) != 1 || !strings.Contains(env[0].texto, "Horario de atención") {
		t.Fatalf("«1» debe traer el horario del pod: %+v", env)
	}
	// Sin run: la etiqueta tal cual (teléfono que manda solo el texto de la
	// fila, con emoji y sin tildes) elige la opción desde el inicio.
	_, env = b.mensaje("51999000020", "consultar STOCK", "")
	if len(env) != 1 || env[0].texto != "¿Qué producto buscas?" {
		t.Fatalf("la etiqueta elige «Consultar stock»: %+v", env)
	}
	_, env = b.mensaje("51999000020", "Paracetamol", "")
	if len(env) != 1 || !strings.Contains(env[0].texto, "Paracetamol — 40") {
		t.Fatalf("el producto va a la consulta de stock: %+v", env)
	}
	if got := b.pod.datos[len(b.pod.datos)-1]; got != "stock=paracetamol" {
		t.Fatalf("la consulta recibe el producto: %q", got)
	}
}

func TestMenuClientesNoEntendiYBotonViejo(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.mensaje("51999000021", "hola", "")
	_, env := b.mensaje("51999000021", "qué tal", "")
	if len(env) != 2 || env[0].texto != "No te entendí 🙈" || !strings.Contains(env[1].texto, "*1.*") {
		t.Fatalf("no entendí + menú: %+v", env)
	}
	_, env = b.mensaje("51999000021", "9", "")
	if len(env) != 2 || env[0].texto != "Esa opción no está en la lista." {
		t.Fatalf("número fuera de rango: %+v", env)
	}
	// Un botón de un mensaje anterior lleva a su destino.
	_, env = b.mensaje("51999000021", "", "wa2n:2")
	if len(env) != 1 || env[0].texto != "¿Qué producto buscas?" {
		t.Fatalf("el id wa2n:2 lleva a stock: %+v", env)
	}
	// «menú» vuelve al inicio desde cualquier paso.
	_, env = b.mensaje("51999000021", "Menú", "")
	if len(env) != 1 || !strings.Contains(env[0].texto, "*3.* Hablar con una persona") {
		t.Fatalf("«menú» vuelve al inicio: %+v", env)
	}
}

func TestMenuGerenciaTextoLibreYEspera(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("ger", "gerencia", flow_model.AudienciaGerencia, true, menuGerencia())
	num := "51900000030"
	b.pod.roles[num] = map[string]any{"audiencia": "gerencia", "permiso": true}

	// Texto libre sin run: va directo al asistente (como el bot clásico).
	b.pod.consultas["gerencia"] = map[string]any{"texto": "Hoy vendiste S/ 1,200.00"}
	d, env := b.mensaje(num, "¿Cuánto vendió Juan Pérez hoy?", "")
	if !d.Atiende || len(env) != 1 || env[0].texto != "Hoy vendiste S/ 1,200.00" {
		t.Fatalf("texto libre al asistente: %+v %+v", d, env)
	}
	if got := b.pod.datos[0]; got != "gerencia=¿Cuánto vendió Juan Pérez hoy?" {
		t.Fatalf("el asistente recibe la pregunta tal como se escribió: %q", got)
	}

	// Toque en «Caja» (id wa2g:2): consulta que deja al pod esperando.
	b.pod.consultas["gerencia:2"] = map[string]any{"texto": "💵 Caja abierta", "espera": true}
	_, env = b.mensaje(num, "", "wa2g:2")
	if len(env) != 1 || env[0].texto != "💵 Caja abierta" {
		t.Fatalf("opción 2 del dueño: %+v", env)
	}
	// «detalle» sigue en la consulta (conversación con el asistente).
	b.pod.consultas["gerencia:2"] = map[string]any{"texto": "💵 Caja — detalle"}
	_, env = b.mensaje(num, "detalle", "")
	if len(env) != 1 || env[0].texto != "💵 Caja — detalle" {
		t.Fatalf("«detalle» vuelve al pod: %+v", env)
	}
	// El asistente pide el menú: se muestra el del flujo.
	b.pod.consultas["gerencia"] = map[string]any{"texto": "🤔 No te entendí.", "menu": true}
	_, env = b.mensaje(num, "asdf", "")
	if len(env) != 2 || env[0].texto != "🤔 No te entendí." || env[1].kind != "lista" {
		t.Fatalf("menu:true muestra el menú del flujo: %+v", env)
	}
}

func TestConsultaDerivarCierraElRun(t *testing.T) {
	b := nuevoBanco(t)
	def := Definicion{Inicio: "menu", Pasos: []Paso{
		{Clave: "menu", Tipo: TipoMenu, Texto: "Hola", Opciones: []Opcion{{Id: "p", Etiqueta: "Pedido", IrA: "pide"}}},
		{Clave: "pide", Tipo: TipoEspera, Texto: "¿Qué necesitas?", Variable: "pedido", Siguiente: "pedido"},
		{Clave: "pedido", Tipo: TipoConsulta, Accion: "pedido", Dato: "{{pedido}}"},
	}}
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, def)
	b.pod.consultas["pedido"] = map[string]any{"texto": "Anoté tu pedido", "derivar": true}
	b.mensaje("51999000040", "1", "")
	_, env := b.mensaje("51999000040", "paracetamol", "")
	if len(env) != 1 || env[0].texto != "Anoté tu pedido" {
		t.Fatalf("pedido: %+v", env)
	}
	if run, err := b.repo.RunActivo(context.Background(), b.inst.Id, "51999000040"); err == nil && run != nil {
		t.Fatalf("derivar cierra el run")
	}
}

func TestConsultaSinPodSeDisculpaYSigue(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	d := b.svc.Decidir(context.Background(), b.inst, "51999000050", "hola", "")
	if err := b.svc.Atender(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	b.pod.caido = true
	reiniciarRol()
	// Run en curso: sigue aunque el pod no conteste, y la consulta se disculpa.
	_, env := b.mensaje("51999000050", "1", "")
	if len(env) != 1 || env[0].texto != disculpaConsulta {
		t.Fatalf("sin pod, disculpa: %+v", env)
	}
}

func TestValidarMenuYConsulta(t *testing.T) {
	svc := NewFlowService(&repoFalso{}, &senderFalso{}, nil)
	if err := svc.ValidarDefinicion(menuClientes()); err != nil {
		t.Fatalf("válido: %v", err)
	}
	mala := menuClientes()
	mala.Pasos[1].Accion = "inventada"
	if err := svc.ValidarDefinicion(mala); err == nil || !strings.Contains(err.Error(), "consulta desconocida") {
		t.Fatalf("consulta desconocida: %v", err)
	}
	mala = menuGerencia()
	mala.Pasos[0].Defecto = "no_existe"
	if err := svc.ValidarDefinicion(mala); err == nil || !strings.Contains(err.Error(), "defecto") {
		t.Fatalf("defecto inexistente: %v", err)
	}
	// Menú numerado sin tope de 10.
	muchas := Definicion{Inicio: "m", Pasos: []Paso{{Clave: "m", Tipo: TipoMenu, Texto: "x"}, {Clave: "f", Tipo: TipoMensaje, Texto: "ok"}}}
	for i := 0; i < 12; i++ {
		muchas.Pasos[0].Opciones = append(muchas.Pasos[0].Opciones, Opcion{Id: "o" + string(rune('a'+i)), Etiqueta: "Opción", IrA: "f"})
	}
	if err := svc.ValidarDefinicion(muchas); err != nil {
		t.Fatalf("12 opciones en texto: %v", err)
	}
	if !consultaConocida("gerencia:12") || consultaConocida("gerencia:13") {
		t.Fatalf("gerencia:N de 1 a 12")
	}
}

func TestConNombreDeInstancia(t *testing.T) {
	var visto map[string]any
	cb := ConNombreDeInstancia(func(_ context.Context, _ string, p map[string]any) (map[string]any, error) {
		visto = p
		return map[string]any{}, nil
	}, func(id string) string {
		if id == "uuid-1" {
			return "botica"
		}
		return ""
	})
	_, _ = cb(context.Background(), "humano", map[string]any{"instancia": "uuid-1"})
	if visto["instancia_nombre"] != "botica" {
		t.Fatalf("debe añadir el nombre: %v", visto)
	}
	_, _ = cb(context.Background(), "humano", map[string]any{"instancia": "otro"})
	if _, hay := visto["instancia_nombre"]; hay {
		t.Fatalf("sin nombre conocido no se inventa: %v", visto)
	}
}

func TestOrquestadorCacheDeRol(t *testing.T) {
	b := nuevoBanco(t)
	b.flujo("cli", "menu", flow_model.AudienciaCliente, true, menuClientes())
	b.svc.Decidir(context.Background(), b.inst, "51999000060", "hola", "")
	b.svc.Decidir(context.Background(), b.inst, "51999000060", "hola", "")
	if n := b.pod.cuantas("rol"); n != 1 {
		t.Fatalf("mismo texto en 10 s: un solo callback (hubo %d)", n)
	}
	b.svc.Decidir(context.Background(), b.inst, "51999000060", "otra", "")
	if n := b.pod.cuantas("rol"); n != 2 {
		t.Fatalf("texto distinto: otro callback (hubo %d)", n)
	}
	_ = time.Second
}
