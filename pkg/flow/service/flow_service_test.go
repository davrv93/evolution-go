package flow_service

// Pruebas sin red ni base real: repositorio y envío falsos. Nada envía nada.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	flow_repository "github.com/evolution-foundation/evolution-go/pkg/flow/repository"
)

type repoFalso struct {
	defs map[string]flow_model.FlowDef
	runs map[string]*flow_model.FlowRun
}

func (r *repoFalso) DefsPorInstancia(_ context.Context, _ string) ([]flow_model.FlowDef, error) {
	out := []flow_model.FlowDef{}
	for _, d := range r.defs {
		out = append(out, d)
	}
	return out, nil
}

func (r *repoFalso) DefPorID(_ context.Context, id, _ string) (*flow_model.FlowDef, error) {
	d, ok := r.defs[id]
	if !ok {
		return nil, errors.New("no existe")
	}
	return &d, nil
}

func (r *repoFalso) CrearDef(_ context.Context, def *flow_model.FlowDef) error {
	r.defs[def.Id] = *def
	return nil
}

func (r *repoFalso) ActualizarDef(_ context.Context, def *flow_model.FlowDef) error {
	r.defs[def.Id] = *def
	return nil
}

func (r *repoFalso) EliminarDef(_ context.Context, id, _ string) error {
	delete(r.defs, id)
	return nil
}

func (r *repoFalso) RunActivo(_ context.Context, _, remitente string) (*flow_model.FlowRun, error) {
	for _, run := range r.runs {
		if run.Remitente == remitente && run.Estado == flow_model.RunEnCurso {
			return run, nil
		}
	}
	return nil, errors.New("sin run")
}

func (r *repoFalso) CrearRun(_ context.Context, run *flow_model.FlowRun) error {
	if run.Id == "" {
		run.Id = "run-" + run.Remitente
	}
	r.runs[run.Id] = run
	return nil
}

func (r *repoFalso) GuardarRun(_ context.Context, run *flow_model.FlowRun) error {
	r.runs[run.Id] = run
	return nil
}

func (r *repoFalso) MarcarRun(_ context.Context, id, estado string) error {
	if run, ok := r.runs[id]; ok {
		run.Estado = estado
		run.UpdatedAt = time.Now()
	}
	return nil
}

func (r *repoFalso) RunsPorFlow(_ context.Context, flowID, _ string, _ int) ([]flow_model.FlowRun, error) {
	return nil, nil
}

var _ flow_repository.FlowRepository = (*repoFalso)(nil)

type envioRegistrado struct {
	kind   string
	numero string
	texto  string
}

type senderFalso struct {
	envios []envioRegistrado
	falla  bool
}

func (s *senderFalso) Texto(_ *instance_model.Instance, numero, texto string) error {
	if s.falla {
		return errors.New("caído")
	}
	s.envios = append(s.envios, envioRegistrado{kind: "texto", numero: numero, texto: texto})
	return nil
}

func (s *senderFalso) Botones(_ *instance_model.Instance, numero, _, descripcion, _ string, _ []BotonDTO) error {
	if s.falla {
		return errors.New("caído")
	}
	s.envios = append(s.envios, envioRegistrado{kind: "botones", numero: numero, texto: descripcion})
	return nil
}

func (s *senderFalso) Lista(_ *instance_model.Instance, numero, _, descripcion, _, _ string, _ []SeccionDTO) error {
	if s.falla {
		return errors.New("caído")
	}
	s.envios = append(s.envios, envioRegistrado{kind: "lista", numero: numero, texto: descripcion})
	return nil
}

// Flujo de encuesta en 2 pasos: mensaje de bienvenida → botones → fin.
func definicionEncuesta() Definicion {
	return Definicion{
		Inicio: "hola",
		Pasos: []Paso{
			{Clave: "hola", Tipo: TipoMensaje, Icono: "🏪", Texto: "¿En qué te ayudo?", Siguiente: "pregunta"},
			{Clave: "pregunta", Tipo: TipoBotones, Texto: "Elige:", Respaldo: "Responde stock u horario",
				Botones: []Opcion{
					{Id: "stock", Etiqueta: "Ver stock", IrA: "fin_stock"},
					{Id: "horario", Etiqueta: "Horario", IrA: "fin_horario"},
				}},
			{Clave: "fin_stock", Tipo: TipoMensaje, Texto: "Dime el producto."},
			{Clave: "fin_horario", Tipo: TipoMensaje, Texto: "De 8 a 22."},
		},
	}
}

func armarEncuesta(t *testing.T) (*repoFalso, *senderFalso, FlowService, *instance_model.Instance) {
	t.Helper()
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := definicionEncuesta()
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatalf("la encuesta de prueba debe ser válida: %v", err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["f1"] = flow_model.FlowDef{Id: "f1", InstanceID: "inst1", Nombre: "Encuesta", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	return repo, sender, svc, &instance_model.Instance{Id: "inst1"}
}

func TestDisparoPorEntradaEnviaBienvenidaYBotones(t *testing.T) {
	_, sender, svc, inst := armarEncuesta(t)
	ok, err := svc.Evaluar(context.Background(), inst, "51999", "hola", "")
	if err != nil || !ok {
		t.Fatalf("debió consumir la entrada: ok=%v err=%v", ok, err)
	}
	if len(sender.envios) != 2 || sender.envios[0].kind != "texto" || sender.envios[1].kind != "botones" {
		t.Fatalf("debió enviar bienvenida + botones, envió %+v", sender.envios)
	}
	if !strings.HasPrefix(sender.envios[0].texto, "🏪") {
		t.Fatalf("el icono debe ir delante del texto: %q", sender.envios[0].texto)
	}
}

func TestBotonValidoAvanzaYBotonInvalidoRepite(t *testing.T) {
	_, sender, svc, inst := armarEncuesta(t)
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51999", "hola", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	ok, err := svc.Evaluar(ctx, inst, "51999", "", "horario")
	if err != nil || !ok {
		t.Fatalf("botón válido: ok=%v err=%v", ok, err)
	}
	if len(sender.envios) != 1 || sender.envios[0].texto != "De 8 a 22." {
		t.Fatalf("debió cerrar con el horario, envió %+v", sender.envios)
	}

	// Otro remitente: elige algo que no existe → se repite el paso de botones.
	if _, err := svc.Evaluar(ctx, inst, "51888", "hola", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51888", "otra cosa", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.envios) != 1 || sender.envios[0].kind != "botones" {
		t.Fatalf("debió repetir los botones, envió %+v", sender.envios)
	}
}

func TestEsperaValidaNumeroYAgotaReintentos(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := Definicion{Inicio: "pedir", Pasos: []Paso{
		{Clave: "pedir", Tipo: TipoEspera, Texto: "¿Cuántos?", Variable: "cantidad", Validacion: "numero", Reintentos: 1, Siguiente: "fin"},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Anotado."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["f2"] = flow_model.FlowDef{Id: "f2", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "pedido", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()

	if _, err := svc.Evaluar(ctx, inst, "51777", "pedido", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	// "muchos" no es número → mensaje de error, sigue en el paso.
	if _, err := svc.Evaluar(ctx, inst, "51777", "muchos", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.envios) != 1 || sender.envios[0].texto != "No te entendí. ¿Me lo dices de nuevo?" {
		t.Fatalf("debió pedir de nuevo, envió %+v", sender.envios)
	}
	// Segundo fallo supera Reintentos=1 → abandona con despedida.
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51777", "tampoco", ""); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.RunActivo(ctx, "inst1", "51777")
	if run != nil {
		t.Fatal("el run debió quedar abandonado")
	}
	// "5" ahora es mensaje nuevo sin flujo → no se consume.
	ok, err := svc.Evaluar(ctx, inst, "51777", "5", "")
	if err != nil || ok {
		t.Fatalf("sin run ni entrada no se consume: ok=%v err=%v", ok, err)
	}
}

func TestCondicionRamificaPorVariable(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := Definicion{Inicio: "pedir", Pasos: []Paso{
		{Clave: "pedir", Tipo: TipoEspera, Texto: "¿Socio? (si/no)", Variable: "socio", Validacion: "si_no", Siguiente: "ruteo"},
		{Clave: "ruteo", Tipo: TipoCondicion, Reglas: []Regla{{Variable: "socio", Es: "si", IrA: "vip"}}, Defecto: "comun"},
		{Clave: "vip", Tipo: TipoMensaje, Texto: "Precio socio."},
		{Clave: "comun", Tipo: TipoMensaje, Texto: "Precio lista."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["f3"] = flow_model.FlowDef{Id: "f3", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "precio", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()

	if _, err := svc.Evaluar(ctx, inst, "51666", "precio", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51666", "si", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.envios) != 1 || sender.envios[0].texto != "Precio socio." {
		t.Fatalf("debió ir a la rama socio, envió %+v", sender.envios)
	}
}

func TestRunVencidoNoSeRetoma(t *testing.T) {
	repo, _, svc, inst := armarEncuesta(t)
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51555", "hola", ""); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.RunActivo(ctx, "inst1", "51555")
	if run == nil {
		t.Fatal("debió existir el run")
	}
	run.UpdatedAt = time.Now().Add(-2 * time.Hour)
	if _, err := svc.Evaluar(ctx, inst, "51555", "stock", ""); err != nil {
		t.Fatal(err)
	}
	// "stock" no es entrada: el run vencido se abandona y nada lo consume.
	run2, _ := repo.RunActivo(ctx, "inst1", "51555")
	if run2 != nil {
		t.Fatal("el run vencido debió abandonarse")
	}
}

func TestValidacionRechazaBotonesSinRespaldoYTiposFuturos(t *testing.T) {
	svc := NewFlowService(&repoFalso{}, &senderFalso{}, nil)
	sinRespaldo := Definicion{Inicio: "a", Pasos: []Paso{
		{Clave: "a", Tipo: TipoBotones, Texto: "Elige", Botones: []Opcion{{Id: "x", Etiqueta: "X", IrA: "b"}}},
		{Clave: "b", Tipo: TipoMensaje, Texto: "Fin"},
	}}
	if err := svc.ValidarDefinicion(sinRespaldo); err == nil {
		t.Fatal("botones sin respaldo deben rechazarse")
	}
	futuro := Definicion{Inicio: "a", Pasos: []Paso{{Clave: "a", Tipo: "ia_gemini", Texto: "Hola"}, {Clave: "b", Tipo: TipoMensaje, Texto: "Fin"}}}
	if err := svc.ValidarDefinicion(futuro); err == nil {
		t.Fatal("ia_gemini es Fase 3 y debe rechazarse en Fase 1")
	}
	hyper := Definicion{Inicio: "a", Pasos: []Paso{{Clave: "a", Tipo: TipoMensaje, Texto: "x", Siguiente: "noexiste"}}}
	if err := svc.ValidarDefinicion(hyper); err == nil {
		t.Fatal("destino inexistente debe rechazarse")
	}
}
