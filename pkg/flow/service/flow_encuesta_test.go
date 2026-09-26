package flow_service

// Paso encuesta, votos, resultados, {{variables}} y envío en segundo plano.
// Sin red ni WhatsApp: repositorio y envío falsos.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"go.mau.fi/whatsmeow"
)

// senderEncuestaFalso suma encuestas al falso de siempre.
type senderEncuestaFalso struct {
	senderFalso
	mu        sync.Mutex
	encuestas []envioEncuesta
	n         int
}

type envioEncuesta struct {
	numero, pregunta string
	opciones         []string
	id               string
}

func (s *senderEncuestaFalso) Encuesta(_ *instance_model.Instance, numero, pregunta string, opciones []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	id := fmt.Sprintf("POLL%d", s.n)
	s.encuestas = append(s.encuestas, envioEncuesta{numero: numero, pregunta: pregunta, opciones: opciones, id: id})
	return id, nil
}

func defEncuestaNativa() Definicion {
	return Definicion{
		Inicio: "nombre",
		Pasos: []Paso{
			{Clave: "nombre", Tipo: TipoEspera, Texto: "¿Cómo te llamas?", Variable: "cliente", Siguiente: "atencion"},
			{Clave: "atencion", Tipo: TipoEncuesta, Texto: "{{cliente}}, ¿cómo te atendimos?", Opciones: []Opcion{
				{Id: "bien", Etiqueta: "Bien", IrA: "gracias"},
				{Id: "regular", Etiqueta: "Regular", IrA: "mejorar"},
				{Id: "mal", Etiqueta: "Mal", IrA: "mejorar"},
			}},
			{Clave: "gracias", Tipo: TipoMensaje, Texto: "¡Gracias, {{cliente}}!"},
			{Clave: "mejorar", Tipo: TipoMensaje, Texto: "Lo sentimos, {{cliente}}. Vamos a mejorar."},
		},
	}
}

func armarNativa(t *testing.T, def Definicion) (*repoFalso, *senderEncuestaFalso, *flowService, *instance_model.Instance) {
	t.Helper()
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderEncuestaFalso{}
	svc := NewFlowService(repo, sender, nil).(*flowService)
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatalf("la definición de prueba debe ser válida: %v", err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fe"] = flow_model.FlowDef{Id: "fe", InstanceID: "inst1", Nombre: "Atención", Estado: flow_model.EstadoActivo, Entrada: "encuesta", Steps: steps}
	return repo, sender, svc, &instance_model.Instance{Id: "inst1"}
}

func TestHashOpcionEsElDeWhatsmeow(t *testing.T) {
	for _, o := range []string{"Sí", "No", "Regular 😐", "Muy bien, gracias"} {
		if !bytes.Equal(HashOpcion(o), whatsmeow.HashPollOptions([]string{o})[0]) {
			t.Fatalf("hash distinto para %q", o)
		}
	}
}

func TestValidarEncuesta(t *testing.T) {
	svc := NewFlowService(&repoFalso{}, &senderFalso{}, nil)
	con := func(ops []Opcion, texto string) error {
		pasos := []Paso{{Clave: "e", Tipo: TipoEncuesta, Texto: texto, Opciones: ops}, {Clave: "fin", Tipo: TipoMensaje, Texto: "ok"}}
		return svc.ValidarDefinicion(Definicion{Inicio: "e", Pasos: pasos})
	}
	op := func(i int) Opcion {
		return Opcion{Id: fmt.Sprintf("o%d", i), Etiqueta: fmt.Sprintf("Opción %d", i), IrA: "fin"}
	}
	var doce, trece []Opcion
	for i := 1; i <= 13; i++ {
		if i <= 12 {
			doce = append(doce, op(i))
		}
		trece = append(trece, op(i))
	}
	if err := con(doce, "¿Pregunta?"); err != nil {
		t.Fatalf("12 opciones es el tope y vale: %v", err)
	}
	casos := map[string]error{
		"13 opciones":        con(trece, "¿Pregunta?"),
		"una sola opción":    con(doce[:1], "¿Pregunta?"),
		"sin pregunta":       con(doce[:2], "  "),
		"pregunta enorme":    con(doce[:2], strings.Repeat("a", 256)),
		"texto repetido":     con([]Opcion{op(1), {Id: "x", Etiqueta: " opción 1 ", IrA: "fin"}}, "¿P?"),
		"id repetido":        con([]Opcion{op(1), {Id: "o1", Etiqueta: "Otra", IrA: "fin"}}, "¿P?"),
		"sin destino":        con([]Opcion{op(1), {Id: "x", Etiqueta: "Otra"}}, "¿P?"),
		"destino inválido":   con([]Opcion{op(1), {Id: "x", Etiqueta: "Otra", IrA: "nada"}}, "¿P?"),
		"variable en opción": con([]Opcion{op(1), {Id: "x", Etiqueta: "{{nombre}}", IrA: "fin"}}, "¿P?"),
		"opción de 101":      con([]Opcion{op(1), {Id: "x", Etiqueta: strings.Repeat("b", 101), IrA: "fin"}}, "¿P?"),
	}
	for nombre, err := range casos {
		if err == nil {
			t.Errorf("%s debió rechazarse", nombre)
		}
	}
}

func TestEncuestaNativaDeFinAFin(t *testing.T) {
	repo, sender, svc, inst := armarNativa(t, defEncuestaNativa())
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51999", "encuesta", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Evaluar(ctx, inst, "51999", "Ana", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.encuestas) != 1 {
		t.Fatalf("debió salir una encuesta nativa, salieron %d", len(sender.encuestas))
	}
	e := sender.encuestas[0]
	if e.pregunta != "ana, ¿cómo te atendimos?" || strings.Join(e.opciones, "|") != "Bien|Regular|Mal" {
		// El texto del entrante llega normalizado (minúsculas) al contexto.
		t.Fatalf("pregunta u opciones mal: %+v", e)
	}
	run := repo.runs["run-51999"]
	if mapaContexto(run.Contexto)["encuesta_atencion"] != "POLL1" {
		t.Fatalf("el id de la encuesta debe quedar en el contexto: %s", run.Contexto)
	}

	// Voto a OTRA encuesta: se ignora.
	if ok, _ := svc.Votar(ctx, inst, "51999", "POLL-VIEJA", [][]byte{HashOpcion("Bien")}); ok {
		t.Fatal("un voto a otra encuesta no debe mover el run")
	}
	// Hash desconocido: se ignora.
	if ok, _ := svc.Votar(ctx, inst, "51999", "POLL1", [][]byte{HashOpcion("Excelente")}); ok {
		t.Fatal("un hash que no es opción no debe mover el run")
	}
	sender.envios = nil
	ok, err := svc.Votar(ctx, inst, "51999", "POLL1", [][]byte{HashOpcion("Regular")})
	if err != nil || !ok {
		t.Fatalf("el voto válido debe avanzar: ok=%v err=%v", ok, err)
	}
	if len(sender.envios) != 1 || sender.envios[0].texto != "Lo sentimos, ana. Vamos a mejorar." {
		t.Fatalf("debió seguir al destino de «Regular» con la variable expandida: %+v", sender.envios)
	}
	vars := mapaContexto(run.Contexto)
	if vars["opcion_atencion"] != "regular" || run.Estado != flow_model.RunCompletado {
		t.Fatalf("elección y cierre: %+v estado=%s", vars, run.Estado)
	}
}

func TestEncuestaAceptaNumeroEscritoYRecuerdaSinReenviar(t *testing.T) {
	repo, sender, svc, inst := armarNativa(t, defEncuestaNativa())
	ctx := context.Background()
	_, _ = svc.Evaluar(ctx, inst, "51888", "encuesta", "")
	_, _ = svc.Evaluar(ctx, inst, "51888", "Luis", "")
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51888", "no sé", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.encuestas) != 1 {
		t.Fatal("un texto que no es opción no reenvía la encuesta")
	}
	if len(sender.envios) != 1 || !strings.Contains(sender.envios[0].texto, "1. Bien") || !strings.Contains(sender.envios[0].texto, "3. Mal") {
		t.Fatalf("debe recordar las opciones numeradas: %+v", sender.envios)
	}
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51888", "1", ""); err != nil {
		t.Fatal(err)
	}
	if mapaContexto(repo.runs["run-51888"].Contexto)["opcion_atencion"] != "bien" {
		t.Fatal("«1» es la primera opción")
	}
}

func TestVotoCambiadoActualizaSinReencaminar(t *testing.T) {
	def := defEncuestaNativa()
	// Tras votar, el flujo se queda esperando otra cosa: el run sigue vivo.
	def.Pasos[2] = Paso{Clave: "gracias", Tipo: TipoEspera, Texto: "¿Algo más?", Variable: "extra"}
	def.Pasos[3] = Paso{Clave: "mejorar", Tipo: TipoEspera, Texto: "¿Qué mejoramos?", Variable: "extra"}
	repo, sender, svc, inst := armarNativa(t, def)
	ctx := context.Background()
	_, _ = svc.Evaluar(ctx, inst, "51777", "encuesta", "")
	_, _ = svc.Evaluar(ctx, inst, "51777", "Eva", "")
	if ok, _ := svc.Votar(ctx, inst, "51777", "POLL1", [][]byte{HashOpcion("Bien")}); !ok {
		t.Fatal("primer voto")
	}
	sender.envios = nil
	ok, err := svc.Votar(ctx, inst, "51777", "POLL1", [][]byte{HashOpcion("Mal")})
	if err != nil || !ok {
		t.Fatalf("el cambio de voto se registra: ok=%v err=%v", ok, err)
	}
	run := repo.runs["run-51777"]
	if mapaContexto(run.Contexto)["opcion_atencion"] != "mal" || run.StepActual != "gracias" || len(sender.envios) != 0 {
		t.Fatalf("cambia el resultado, no el camino ni envía nada: paso=%s envios=%+v", run.StepActual, sender.envios)
	}
}

func TestEncuestaEsperaMasQueElTTL(t *testing.T) {
	repo, _, svc, inst := armarNativa(t, defEncuestaNativa())
	ctx := context.Background()
	_, _ = svc.Evaluar(ctx, inst, "51666", "encuesta", "")
	_, _ = svc.Evaluar(ctx, inst, "51666", "Rosa", "")
	run := repo.runs["run-51666"]
	run.UpdatedAt = time.Now().Add(-5 * time.Hour) // más que 30 min, menos que 72 h
	if ok, _ := svc.Votar(ctx, inst, "51666", "POLL1", [][]byte{HashOpcion("Bien")}); !ok {
		t.Fatal("una encuesta se puede contestar horas después")
	}

	_, _ = svc.Evaluar(ctx, inst, "51555", "encuesta", "")
	vieja := repo.runs["run-51555"] // parado en «nombre» (espera), no en la encuesta
	vieja.UpdatedAt = time.Now().Add(-2 * time.Hour)
	if ok, _ := svc.Votar(ctx, inst, "51555", "", [][]byte{HashOpcion("Bien")}); ok {
		t.Fatal("un run vencido fuera de encuesta no acepta votos")
	}
	if vieja.Estado != flow_model.RunAbandonado {
		t.Fatalf("el run vencido se abandona, quedó %s", vieja.Estado)
	}
}

func TestEncuestaSinSenderEncuestaSaleComoTexto(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := defEncuestaNativa()
	steps, _ := json.Marshal(def)
	repo.defs["fe"] = flow_model.FlowDef{Id: "fe", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "encuesta", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	_, _ = svc.Evaluar(context.Background(), inst, "51444", "encuesta", "")
	_, _ = svc.Evaluar(context.Background(), inst, "51444", "Teo", "")
	ult := sender.envios[len(sender.envios)-1]
	if ult.kind != "texto" || !strings.Contains(ult.texto, "2. Regular") {
		t.Fatalf("sin encuestas nativas degrada a texto numerado: %+v", ult)
	}
}

func TestResultadosCuentaPorOpcion(t *testing.T) {
	def := defEncuestaNativa()
	ctxDe := func(m map[string]string) json.RawMessage { b, _ := json.Marshal(m); return b }
	ahora := time.Now()
	runs := []flow_model.FlowRun{
		{Remitente: "a", Estado: flow_model.RunCompletado, UpdatedAt: ahora, Contexto: ctxDe(map[string]string{"encuesta_atencion": "P1", "opcion_atencion": "bien"})},
		{Remitente: "b", Estado: flow_model.RunCompletado, UpdatedAt: ahora.Add(-time.Minute), Contexto: ctxDe(map[string]string{"encuesta_atencion": "P2", "opcion_atencion": "bien"})},
		{Remitente: "c", Estado: flow_model.RunCompletado, UpdatedAt: ahora, Contexto: ctxDe(map[string]string{"encuesta_atencion": "P3", "opcion_atencion": "mal"})},
		{Remitente: "d", Estado: flow_model.RunEnCurso, UpdatedAt: ahora, Contexto: ctxDe(map[string]string{"encuesta_atencion": "P4"})},
		{Remitente: "e", Estado: flow_model.RunEnCurso, UpdatedAt: ahora, Contexto: ctxDe(map[string]string{"opcion_atencion": "inventada"})},
	}
	res := Resultados(def, runs)
	if len(res) != 1 {
		t.Fatalf("una sola pregunta con alternativas, hubo %d", len(res))
	}
	p := res[0]
	if p.Enviadas != 4 || p.Respuestas != 3 || p.Opciones[0].Votos != 2 || p.Opciones[1].Votos != 0 || p.Opciones[2].Votos != 1 {
		t.Fatalf("conteo mal: %+v", p)
	}
	if p.Opciones[0].Votantes[0].Remitente != "a" {
		t.Fatal("los votantes van del más reciente al más antiguo")
	}
}

// {{variables}}: todo texto saliente se expande con el contexto del run.
func TestVariablesSeExpandenEnTodoTextoSaliente(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := Definicion{Inicio: "producto", Pasos: []Paso{
		{Clave: "producto", Tipo: TipoEspera, Texto: "¿Qué producto buscas?", Variable: "producto", Siguiente: "reviso"},
		{Clave: "reviso", Tipo: TipoMensaje, Texto: "Reviso si tenemos {{producto}}", Siguiente: "cantidad"},
		{Clave: "cantidad", Tipo: TipoEspera, Texto: "¿Para qué fecha separo {{producto}}?", Variable: "cantidad",
			Validacion: "fecha", MensajeError: "Dime una fecha para {{producto}}", Siguiente: "confirmar"},
		{Clave: "confirmar", Tipo: TipoBotones, Texto: "{{cantidad}} de {{producto}}, ¿confirmas?", Respaldo: "sí o no",
			Botones: []Opcion{{Id: "si", Etiqueta: "Sí, {{cantidad}}", IrA: "fin"}, {Id: "no", Etiqueta: "No", IrA: "fin"}}},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Listo."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fv"] = flow_model.FlowDef{Id: "fv", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "pedido", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()
	_, _ = svc.Evaluar(ctx, inst, "51333", "pedido", "")
	_, _ = svc.Evaluar(ctx, inst, "51333", "paracetamol", "")
	_, _ = svc.Evaluar(ctx, inst, "51333", "mañana", "") // no es fecha: mensaje_error
	_, _ = svc.Evaluar(ctx, inst, "51333", "25/12/2026", "")
	var textos []string
	for _, e := range sender.envios {
		textos = append(textos, e.texto)
	}
	todo := strings.Join(textos, "\n")
	for _, esperado := range []string{
		"Reviso si tenemos paracetamol",
		"¿Para qué fecha separo paracetamol?",
		"Dime una fecha para paracetamol",
		"25/12/2026 de paracetamol, ¿confirmas?",
	} {
		if !strings.Contains(todo, esperado) {
			t.Errorf("falta %q en lo enviado:\n%s", esperado, todo)
		}
	}
	if strings.Contains(todo, "{{") {
		t.Fatalf("ninguna llave debe llegar al cliente:\n%s", todo)
	}
	// El texto escrito casa con la etiqueta YA expandida.
	if _, err := svc.Evaluar(ctx, inst, "51333", "sí, 25/12/2026", ""); err != nil {
		t.Fatal(err)
	}
	if mapaContexto(repo.runs["run-51333"].Contexto)["opcion_confirmar"] != "si" {
		t.Fatal("«Sí, 25/12/2026» debe casar con el botón «Sí, {{cantidad}}»")
	}
	// La vista previa sigue enseñando la plantilla.
	vista, _ := svc.VistaPrevia(def)
	if !strings.Contains(vista[1]["resumen"], "{{producto}}") {
		t.Fatal("la vista previa no expande")
	}
}

func TestProgramarContestaYaYDespachaDetras(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderEncuestaFalso{}
	var mu sync.Mutex
	var bitacora []string
	listo := make(chan struct{}, 10)
	cb := func(_ context.Context, accion string, p map[string]any) (map[string]any, error) {
		switch accion {
		case "permiso":
			return map[string]any{"ok": p["remitente"] != "51999000111"}, nil
		case "envio":
			mu.Lock()
			bitacora = append(bitacora, fmt.Sprintf("%s:%s:%s", p["remitente"], p["resultado"], p["resumen"]))
			mu.Unlock()
			if p["resultado"] == EnvioEnviado {
				listo <- struct{}{}
			}
		}
		return map[string]any{"ok": true}, nil
	}
	svc := NewFlowService(repo, sender, cb)
	def := Definicion{Inicio: "e", Pasos: []Paso{
		{Clave: "e", Tipo: TipoEncuesta, Texto: "¿Volverías?", Opciones: []Opcion{{Id: "si", Etiqueta: "Sí", IrA: "f"}, {Id: "no", Etiqueta: "No", IrA: "f"}}},
		{Clave: "f", Tipo: TipoMensaje, Texto: "Gracias"},
	}}
	steps, _ := json.Marshal(def)
	rec := &flow_model.FlowDef{Id: "fp", InstanceID: "inst1", Nombre: "Volverías", Estado: flow_model.EstadoActivo, Entrada: "x", Steps: steps}
	repo.defs["fp"] = *rec
	inst := &instance_model.Instance{Id: "inst1"}

	inicio := time.Now()
	cuenta, detalle := svc.Programar(context.Background(), inst, rec,
		[]string{"51999000222", "51999000111", "abc", "51999000333"}, 50*time.Millisecond)
	if time.Since(inicio) > 40*time.Millisecond {
		t.Fatal("Programar no debe esperar a los envíos")
	}
	if cuenta["programados"] != 2 || cuenta["vetados"] != 1 || cuenta["invalidos"] != 1 || len(detalle) != 4 {
		t.Fatalf("conteo mal: %+v %+v", cuenta, detalle)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-listo:
		case <-time.After(3 * time.Second):
			t.Fatal("el despacho en segundo plano no terminó")
		}
	}
	sender.mu.Lock()
	n := len(sender.encuestas)
	sender.mu.Unlock()
	if n != 2 {
		t.Fatalf("debieron salir 2 encuestas, salieron %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	todo := strings.Join(bitacora, "\n")
	if !strings.Contains(todo, "51999000111:vetado") || !strings.Contains(todo, "51999000222:enviado:¿Volverías?") {
		t.Fatalf("la bitácora del pod debe recibir vetados y enviados:\n%s", todo)
	}
}
