package flow_service

// Pasos y reglas de los MENÚS convertidos en flujo (menú de clientes y menú
// gerencial). Dos tipos de paso nuevos y un enganche en `continuar`:
//
//   - `menu`: menú numerado en texto («*1.* Horario…»), sin tope de 10 como
//     la lista de WhatsApp. Es el formato «texto» del menú clásico y el que
//     se usa con más de 10 opciones.
//   - `consulta`: la resuelve el pod por callback («consulta») con la MISMA
//     función que usa el menú clásico (stock, horario, pedido, asistente de
//     gerencia). El pod puede pedir que el run siga esperando en este paso
//     (`espera`): así un «¿Qué producto?» del asistente recibe su respuesta.
//
// En un flujo por defecto (FlowDef.PorDefecto) además:
//   - «menú», «0», «inicio», un saludo… vuelven al inicio en cualquier paso;
//   - una opción se elige por id (botón), por su etiqueta exacta (teléfonos
//     que devuelven solo el texto de la fila) o por su número;
//   - el id de un botón de un mensaje anterior («wa2n:12», «wa2g:3») lleva a
//     su destino aunque el run esté en otro paso;
//   - lo que no casa con ninguna opción va al `defecto` del paso si lo tiene
//     (texto libre al asistente de gerencia) o recibe «No te entendí» y el
//     menú de inicio, como el menú clásico.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

const (
	TipoConsulta = "consulta"
	TipoMenu     = "menu"
)

// claveTextoLibre guarda en el contexto el texto que el cliente escribió y
// que viaja a la consulta siguiente (texto libre al asistente). Se borra al
// consumirse.
const claveTextoLibre = "_texto"

// El motor compara en minúsculas (normalizar), pero lo que viaja al pod en
// una consulta es lo que el cliente ESCRIBIÓ: «cotiza 10 paracetamol para
// Juan Pérez» no puede llegar como «juan pérez». Atender lo deja en el ctx.
type claveCrudo struct{}

func conTextoCrudo(ctx context.Context, texto string) context.Context {
	return context.WithValue(ctx, claveCrudo{}, strings.TrimSpace(texto))
}

// crudo devuelve el texto tal como se escribió si corresponde a `texto`.
func crudo(ctx context.Context, texto string) string {
	if c, ok := ctx.Value(claveCrudo{}).(string); ok && c != "" && normalizar(c) == normalizar(texto) {
		return c
	}
	return strings.TrimSpace(texto)
}

// Pie por defecto del menú numerado: el mismo del menú clásico.
const pieMenuNumerado = "_Responde con el número de la opción._"

// Consultas que el pod sabe resolver (lista cerrada: lo que no está, no
// existe). «gerencia:N» es la opción N del menú del dueño (1-12).
var consultasConocidas = map[string]bool{"stock": true, "horario": true, "pedido": true, "gerencia": true}

func consultaConocida(accion string) bool {
	if consultasConocidas[accion] {
		return true
	}
	if n, ok := strings.CutPrefix(accion, "gerencia:"); ok {
		i, err := strconv.Atoi(n)
		return err == nil && i >= 1 && i <= 12
	}
	return false
}

// plano: minúsculas, sin tildes, sin emojis ni signos, espacios colapsados.
// Es la comparación de etiquetas del menú clásico (etiquetaPlana del pod).
func plano(t string) string {
	t = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ñ", "n", "ü", "u").Replace(strings.ToLower(t))
	var b strings.Builder
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func soloDigitos(t string) bool {
	if t == "" {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

// esPalabraMenu: lo que en el menú clásico (clientes y gerencia) muestra el
// menú de inicio.
func esPalabraMenu(texto string) bool {
	switch plano(texto) {
	case "menu", "inicio", "opciones", "0", "volver", "volver al inicio", "ver menu",
		"hola", "holi", "buenas", "buenos dias", "buenas tardes", "buenas noches":
		return true
	}
	return false
}

// ── Opciones de un paso de menú (botones, lista, menú numerado) ─────────────

type opcionMenu struct {
	Id       string
	Etiqueta string
	IrA      string
}

func esPasoMenu(p *Paso) bool {
	return p != nil && (p.Tipo == TipoBotones || p.Tipo == TipoLista || p.Tipo == TipoMenu)
}

func opcionesMenu(p *Paso) []opcionMenu {
	var out []opcionMenu
	switch p.Tipo {
	case TipoBotones:
		for _, o := range p.Botones {
			out = append(out, opcionMenu{Id: o.Id, Etiqueta: o.Etiqueta, IrA: o.IrA})
		}
	case TipoLista:
		for _, sec := range p.Secciones {
			for _, f := range sec.Filas {
				out = append(out, opcionMenu{Id: f.Id, Etiqueta: f.Titulo, IrA: f.IrA})
			}
		}
	case TipoMenu:
		for _, o := range p.Opciones {
			out = append(out, opcionMenu{Id: o.Id, Etiqueta: o.Etiqueta, IrA: o.IrA})
		}
	}
	return out
}

// resolverOpcion: por id del botón, por etiqueta (sin tildes ni emojis) o
// por número de la opción.
func resolverOpcion(p *Paso, texto, botonID string) (opcionMenu, bool) {
	ops := opcionesMenu(p)
	if id := strings.TrimSpace(botonID); id != "" {
		for _, o := range ops {
			if o.Id == id {
				return o, true
			}
		}
	}
	t := plano(texto)
	if t == "" {
		return opcionMenu{}, false
	}
	for _, o := range ops {
		if plano(o.Etiqueta) == t {
			return o, true
		}
	}
	if soloDigitos(t) {
		if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(ops) {
			return ops[n-1], true
		}
	}
	return opcionMenu{}, false
}

// destinoPorID busca el id de un botón en TODO el flujo: el cliente puede
// tocar un botón de un mensaje anterior.
func destinoPorID(def *Definicion, botonID string) string {
	id := strings.TrimSpace(botonID)
	if id == "" {
		return ""
	}
	for i := range def.Pasos {
		for _, o := range opcionesMenu(&def.Pasos[i]) {
			if o.Id == id && o.IrA != "" {
				return o.IrA
			}
		}
	}
	return ""
}

func esConsulta(def *Definicion, clave string) bool {
	p := buscarPaso(def, clave)
	return p != nil && p.Tipo == TipoConsulta
}

// textoMenu arma el menú numerado como lo escribe el menú clásico.
func textoMenu(p *Paso) string {
	cabecera := p.Texto
	if p.Icono != "" && !strings.HasPrefix(cabecera, p.Icono) {
		cabecera = p.Icono + " " + cabecera
	}
	l := []string{cabecera, ""}
	for i, o := range p.Opciones {
		l = append(l, "*"+strconv.Itoa(i+1)+".* "+o.Etiqueta)
	}
	pie := strings.TrimSpace(p.Pie)
	if pie == "" {
		pie = pieMenuNumerado
	}
	l = append(l, "", pie)
	return strings.Join(l, "\n")
}

func (s *flowService) enviarMenu(inst *instance_model.Instance, remitente string, original *Paso, vars map[string]string) error {
	visto := pasoExpandido(original, vars)
	return s.sender.Texto(inst, remitente, textoMenu(&visto))
}

// reenviar repite un paso de menú tal cual (sin avanzar).
func (s *flowService) reenviar(inst *instance_model.Instance, remitente string, paso *Paso, vars map[string]string) error {
	if paso.Tipo == TipoMenu {
		return s.enviarMenu(inst, remitente, paso, vars)
	}
	return s.enviarPaso(inst, remitente, paso, vars)
}

// ── Enganche en continuar ───────────────────────────────────────────────────

// continuarMenu atiende lo propio de los menús antes que el motor de siempre.
// Devuelve hecho=false cuando no le toca (flujo normal en un paso normal):
// entonces sigue `continuar` sin cambios.
func (s *flowService) continuarMenu(ctx context.Context, inst *instance_model.Instance, rec *flow_model.FlowDef, def *Definicion, run *flow_model.FlowRun, paso *Paso, texto, botonID string) (bool, error) {
	menuFlujo := rec != nil && rec.PorDefecto
	irA := func(clave string) (bool, error) {
		run.StepActual = clave
		return true, s.ejecutarDesde(ctx, inst, rec, def, run, "", "")
	}
	if menuFlujo && botonID == "" && esPalabraMenu(texto) {
		vars := mapaContexto(run.Contexto)
		delete(vars, claveTextoLibre)
		run.Contexto = guardarContexto(vars)
		return irA(def.Inicio)
	}
	switch paso.Tipo {
	case TipoConsulta:
		if destino := destinoPorID(def, botonID); destino != "" {
			return irA(destino)
		}
		seguir, err := s.pasoConsulta(ctx, inst, def, run, paso, texto, false)
		if err != nil || !seguir {
			return true, err
		}
		return true, s.ejecutarDesde(ctx, inst, rec, def, run, "", "")
	case TipoMenu, TipoBotones, TipoLista:
		if paso.Tipo != TipoMenu && !menuFlujo && paso.Defecto == "" {
			return false, nil // botones/lista de un flujo normal: el motor de siempre
		}
		op, ok := resolverOpcion(paso, texto, botonID)
		if !ok && menuFlujo {
			if destino := destinoPorID(def, botonID); destino != "" {
				return irA(destino)
			}
		}
		if ok && op.IrA != "" {
			vars := mapaContexto(run.Contexto)
			vars["opcion_"+paso.Clave] = op.Id // el dato de resultados, como botones/lista
			run.Contexto = guardarContexto(vars)
			return irA(op.IrA)
		}
		return true, s.sinOpcion(ctx, inst, rec, def, run, paso, texto, menuFlujo)
	case TipoEspera:
		// Esperando un dato (p. ej. el nombre del producto) y toca un botón
		// de un menú anterior: manda el botón.
		if menuFlujo {
			if destino := destinoPorID(def, botonID); destino != "" {
				return irA(destino)
			}
		}
	}
	return false, nil
}

// sinOpcion: la respuesta no casa con ninguna opción del paso.
func (s *flowService) sinOpcion(ctx context.Context, inst *instance_model.Instance, rec *flow_model.FlowDef, def *Definicion, run *flow_model.FlowRun, paso *Paso, texto string, menuFlujo bool) error {
	vars := mapaContexto(run.Contexto)
	t := strings.TrimSpace(texto)
	// Texto libre con destino (el asistente de gerencia): viaja a la consulta.
	if paso.Defecto != "" && t != "" && !soloDigitos(plano(t)) {
		if esConsulta(def, paso.Defecto) {
			vars[claveTextoLibre] = crudo(ctx, t)
			run.Contexto = guardarContexto(vars)
		}
		run.StepActual = paso.Defecto
		return s.ejecutarDesde(ctx, inst, rec, def, run, "", "")
	}
	if soloDigitos(plano(t)) {
		if err := s.sender.Texto(inst, run.Remitente, "Esa opción no está en la lista."); err != nil {
			return err
		}
		if err := s.reenviar(inst, run.Remitente, paso, vars); err != nil {
			return err
		}
		tocarRun(run)
		return s.repo.GuardarRun(ctx, run)
	}
	if menuFlujo {
		if err := s.sender.Texto(inst, run.Remitente, "No te entendí 🙈"); err != nil {
			return err
		}
		run.StepActual = def.Inicio
		return s.ejecutarDesde(ctx, inst, rec, def, run, "", "")
	}
	// Flujo normal: se repite el paso, como botones y lista de siempre.
	if err := s.reenviar(inst, run.Remitente, paso, vars); err != nil {
		return err
	}
	tocarRun(run)
	return s.repo.GuardarRun(ctx, run)
}

// ── Inicio de un flujo por defecto ──────────────────────────────────────────

// iniciarMenu arranca el flujo por defecto con el mensaje que lo disparó: un
// botón o una opción del inicio (número o etiqueta) van directo a su
// destino; un texto libre va al `defecto` del inicio si es una consulta
// (asistente de gerencia); lo demás recibe el menú de inicio.
func (s *flowService) iniciarMenu(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, def *Definicion, remitente, texto, botonID string) error {
	run := &flow_model.FlowRun{
		FlowID: d.Id, InstanceID: inst.Id, Remitente: remitente,
		StepActual: def.Inicio, Contexto: []byte(`{}`), Estado: flow_model.RunEnCurso,
	}
	tocarRun(run)
	if err := s.repo.CrearRun(ctx, run); err != nil {
		return err
	}
	s.bitacora("menú por defecto %s (%s, %s) para %s", d.Id, d.Nombre, d.Audiencia, remitente)
	if destino := destinoPorID(def, botonID); destino != "" {
		run.StepActual = destino
		return s.ejecutarDesde(ctx, inst, d, def, run, "", "")
	}
	inicio := buscarPaso(def, def.Inicio)
	t := normalizar(texto)
	if esPasoMenu(inicio) && t != "" && !esPalabraMenu(t) {
		if op, ok := resolverOpcion(inicio, t, ""); ok && op.IrA != "" {
			run.Contexto = guardarContexto(map[string]string{"opcion_" + inicio.Clave: op.Id})
			run.StepActual = op.IrA
			return s.ejecutarDesde(ctx, inst, d, def, run, "", "")
		}
		if inicio.Defecto != "" && esConsulta(def, inicio.Defecto) && !soloDigitos(plano(t)) {
			run.Contexto = guardarContexto(map[string]string{claveTextoLibre: crudo(ctx, t)})
			run.StepActual = inicio.Defecto
			return s.ejecutarDesde(ctx, inst, d, def, run, "", "")
		}
	}
	return s.ejecutarDesde(ctx, inst, d, def, run, "", "")
}

// ── Paso consulta ───────────────────────────────────────────────────────────

func textosDe(resp map[string]any) []string {
	var out []string
	if lista, ok := resp["textos"].([]any); ok {
		for _, v := range lista {
			if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
				out = append(out, strings.TrimSpace(t))
			}
		}
	}
	if len(out) == 0 {
		if t := textoDe(resp); t != "" {
			out = append(out, t)
		}
	}
	return out
}

const disculpaConsulta = "Ahora no pude consultar eso. Escribe *menú* para volver a las opciones."

// pasoConsulta resuelve una consulta en el pod y deja el run donde toca.
// Devuelve seguir=true cuando el run avanzó a otro paso que hay que ejecutar
// (el que llama sigue con ejecutarDesde). Como todo paso de negocio, no
// reintenta: si el pod no responde se dice y la conversación sigue.
//
// nuevo=true: se llegó a la consulta desde otro paso (dato = texto libre
// guardado o plantilla `dato`). nuevo=false: el run ya esperaba aquí y
// `texto` es la respuesta del cliente.
func (s *flowService) pasoConsulta(ctx context.Context, inst *instance_model.Instance, def *Definicion, run *flow_model.FlowRun, paso *Paso, texto string, nuevo bool) (bool, error) {
	vars := mapaContexto(run.Contexto)
	dato := crudo(ctx, texto)
	if nuevo {
		if libre, ok := vars[claveTextoLibre]; ok {
			dato = libre
		} else {
			dato = strings.TrimSpace(plantilla(paso.Dato, vars))
		}
	}
	delete(vars, claveTextoLibre)

	var resp map[string]any
	textos := []string{disculpaConsulta}
	if s.cb != nil {
		r, err := s.cb(ctx, "consulta", map[string]any{
			"accion":    paso.Accion,
			"dato":      dato,
			"nuevo":     nuevo,
			"remitente": run.Remitente,
			"instancia": run.InstanceID,
			"flow_id":   run.FlowID,
			"run_id":    run.Id,
			"paso":      paso.Clave,
			"contexto":  vars,
		})
		if err != nil {
			s.bitacora("consulta %s sin respuesta para %s: %v", paso.Accion, run.Remitente, err)
		} else {
			resp = r
			textos = textosDe(r)
			vars = fusionarContexto(vars, r)
		}
	}
	for _, t := range textos {
		if err := s.sender.Texto(inst, run.Remitente, t); err != nil {
			return false, err
		}
	}
	run.Contexto = guardarContexto(vars)
	marca := func(k string) bool { v, _ := resp[k].(bool); return v }
	switch {
	case marca("derivar"):
		// El pod se quedó la conversación (persona o bot de ventas).
		run.Estado = flow_model.RunDerivado
		s.bitacora("run %s derivado por consulta %s para %s", run.Id, paso.Accion, run.Remitente)
		return false, s.repo.MarcarRun(ctx, run.Id, flow_model.RunDerivado)
	case marca("menu"):
		run.StepActual = def.Inicio
		return true, nil
	case marca("espera"):
		run.StepActual = paso.Clave
		tocarRun(run)
		return false, s.repo.GuardarRun(ctx, run)
	case paso.Siguiente == "":
		run.Estado = flow_model.RunCompletado
		s.bitacora("fin flujo %s para %s (consulta %s)", run.FlowID, run.Remitente, paso.Accion)
		return false, s.repo.MarcarRun(ctx, run.Id, flow_model.RunCompletado)
	default:
		run.StepActual = paso.Siguiente
		return true, nil
	}
}

// ── Validación y vista previa ───────────────────────────────────────────────

func validarPasoMenu(p *Paso, donde string, irA func(clave, donde string) error) error {
	cabe := func(s string, max int) bool { return len([]rune(strings.TrimSpace(s))) <= max }
	switch p.Tipo {
	case TipoConsulta:
		if !consultaConocida(p.Accion) {
			return fmt.Errorf("flujo: %s con consulta desconocida %q", donde, p.Accion)
		}
		if !cabe(p.Dato, 500) {
			return fmt.Errorf("flujo: %s con dato de más de 500 caracteres", donde)
		}
		return irA(p.Siguiente, donde)
	case TipoMenu:
		if strings.TrimSpace(p.Texto) == "" {
			return fmt.Errorf("flujo: %s sin texto", donde)
		}
		if !cabe(p.Texto, 1000) || !cabe(p.Pie, 300) {
			return fmt.Errorf("flujo: %s con texto de más de 1000 caracteres o pie de más de 300", donde)
		}
		if len(p.Opciones) < 1 || len(p.Opciones) > 30 {
			return fmt.Errorf("flujo: %s lleva de 1 a 30 opciones", donde)
		}
		vistos := map[string]bool{}
		for _, o := range p.Opciones {
			if strings.TrimSpace(o.Id) == "" || strings.TrimSpace(o.Etiqueta) == "" {
				return fmt.Errorf("flujo: %s tiene opción sin id o etiqueta", donde)
			}
			if vistos[o.Id] {
				return fmt.Errorf("flujo: %s repite el id de opción %q", donde, o.Id)
			}
			vistos[o.Id] = true
			if !cabe(o.Etiqueta, 60) {
				return fmt.Errorf("flujo: opción %q de %s con etiqueta de más de 60 caracteres", o.Id, donde)
			}
			if o.IrA == "" {
				return fmt.Errorf("flujo: opción %q de %s sin destino", o.Id, donde)
			}
			if err := irA(o.IrA, donde); err != nil {
				return err
			}
		}
		return irA(p.Defecto, donde)
	}
	return nil
}

// validarDefectos: el `defecto` de botones y lista (texto libre) debe
// apuntar a un paso que exista. El de condición y menú ya lo miran ellos.
func validarDefectos(def Definicion, porClave map[string]*Paso) error {
	for i := range def.Pasos {
		p := &def.Pasos[i]
		if (p.Tipo == TipoBotones || p.Tipo == TipoLista) && p.Defecto != "" {
			if _, ok := porClave[p.Defecto]; !ok {
				return fmt.Errorf("flujo: paso %s: el defecto apunta a paso inexistente %q", p.Clave, p.Defecto)
			}
		}
	}
	return nil
}

func resumenMenu(p *Paso) string {
	if p.Tipo == TipoConsulta {
		return "Consulta al pod: " + p.Accion
	}
	return fmt.Sprintf("%s [%d opciones numeradas]", p.Texto, len(p.Opciones))
}
