package flow_service

// Fase 3: pasos de negocio con callback falso y webhook contra httptest.
// Nada sale a la red real.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

func callbackFalso(respuestas map[string]map[string]any, falla string) Callback {
	return func(_ context.Context, accion string, payload map[string]any) (map[string]any, error) {
		if accion == "permiso" {
			return map[string]any{"ok": true}, nil
		}
		if accion == falla {
			return nil, errors.New("pod caído")
		}
		if r, ok := respuestas[accion]; ok {
			return r, nil
		}
		return map[string]any{}, nil
	}
}

func TestIAUsaPlantillaYGuardaVariable(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	var vioPrompt string
	cb := func(_ context.Context, accion string, payload map[string]any) (map[string]any, error) {
		if accion == "permiso" {
			return map[string]any{"ok": true}, nil
		}
		if accion != "ia" {
			return map[string]any{}, nil
		}
		vioPrompt, _ = payload["prompt"].(string)
		return map[string]any{"texto": "Resumen: todo bien."}, nil
	}
	svc := NewFlowService(repo, sender, cb)
	def := Definicion{Inicio: "pedir", Pasos: []Paso{
		{Clave: "pedir", Tipo: TipoEspera, Texto: "¿De qué tema?", Variable: "tema", Validacion: "texto", Siguiente: "ia"},
		{Clave: "ia", Tipo: TipoIA, PromptSistema: "Eres ayuda.", Prompt: "Resume: {{tema}}", VariableSalida: "resumen", Siguiente: "fin"},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Listo."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatalf("definición válida: %v", err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fn"] = flow_model.FlowDef{Id: "fn", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "resumen", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()

	if _, err := svc.Evaluar(ctx, inst, "51999", "resumen", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51999", "ventas", ""); err != nil {
		t.Fatal(err)
	}
	if vioPrompt != "Resume: ventas" {
		t.Fatalf("la plantilla debió resolver {{tema}}, llegó %q", vioPrompt)
	}
	if len(sender.envios) != 2 || sender.envios[0].texto != "Resumen: todo bien." || sender.envios[1].texto != "Listo." {
		t.Fatalf("IA + cierre, envió %+v", sender.envios)
	}
	if run, _ := repo.RunActivo(ctx, "inst1", "51999"); run != nil {
		t.Fatal("debió completarse")
	}
}

func TestPlantillaSustituyeVariables(t *testing.T) {
	vars := map[string]string{"tema": "ventas", "email": "a@b.pe"}
	if got := plantilla("Resume: {{tema}} para {{email}}", vars); got != "Resume: ventas para a@b.pe" {
		t.Fatalf("plantilla: %q", got)
	}
	if got := plantilla("Hola {{ausente}}", vars); got != "Hola " {
		t.Fatalf("ausente queda vacío: %q", got)
	}
}

func TestCorreoAvanzaAunqueFalleSinDuplicar(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, callbackFalso(nil, "correo"))
	def := Definicion{Inicio: "c", Pasos: []Paso{
		{Clave: "c", Tipo: TipoCorreo, Para: "a@b.pe", Asunto: "X", Cuerpo: "Y", Siguiente: "fin"},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Fin."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fc"] = flow_model.FlowDef{Id: "fc", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "mail", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()
	ok, err := svc.Evaluar(ctx, inst, "51888", "mail", "")
	if !ok || err == nil {
		t.Fatalf("el fallo debe surfear como error pero consumir: ok=%v err=%v", ok, err)
	}
	// El run avanzó a "fin" pero el error cortó el envío: sigue en curso ahí
	// (no se reintentó el correo) y se completa con el próximo mensaje.
	run, _ := repo.RunActivo(ctx, "inst1", "51888")
	if run == nil || run.StepActual != "fin" {
		t.Fatalf("el run debió quedar en fin sin reintentar, quedó %+v", run)
	}
	sender.envios = nil
	if _, err := svc.Evaluar(ctx, inst, "51888", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if len(sender.envios) != 1 || sender.envios[0].texto != "Fin." {
		t.Fatalf("debió cerrar con Fin, envió %+v", sender.envios)
	}
	if run, _ := repo.RunActivo(ctx, "inst1", "51888"); run != nil {
		t.Fatal("debió completarse")
	}
}

func TestWebhookDirectoFusionaContexto(t *testing.T) {
	var vioSecreto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vioSecreto = r.Header.Get("X-Flow-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"texto":"n8n dice hola","contexto":{"origen":"n8n"}}`))
	}))
	defer srv.Close()

	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, nil)
	def := Definicion{Inicio: "w", Pasos: []Paso{
		{Clave: "w", Tipo: TipoWebhook, URL: srv.URL, Metodo: "POST", Secreto: "s3cr3t", Siguiente: "fin"},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Fin."},
	}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fw"] = flow_model.FlowDef{Id: "fw", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "n8n", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	if _, err := svc.Evaluar(context.Background(), inst, "51777", "n8n", ""); err != nil {
		t.Fatal(err)
	}
	if vioSecreto != "s3cr3t" {
		t.Fatalf("el secreto debe viajar en cabecera, llegó %q", vioSecreto)
	}
	if len(sender.envios) < 1 || sender.envios[0].texto != "n8n dice hola" {
		t.Fatalf("debió enviar el texto de n8n, envió %+v", sender.envios)
	}
	run, _ := repo.RunActivo(context.Background(), "inst1", "51777")
	if run != nil {
		t.Fatal("debió completarse tras fin")
	}
}

func TestHumanoDerivaYSueltaLaConversacion(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	var avisoPod bool
	cb := func(_ context.Context, accion string, _ map[string]any) (map[string]any, error) {
		if accion == "permiso" {
			return map[string]any{"ok": true}, nil
		}
		if accion == "humano" {
			avisoPod = true
		}
		return map[string]any{}, nil
	}
	svc := NewFlowService(repo, sender, cb)
	def := Definicion{Inicio: "h", Pasos: []Paso{{Clave: "h", Tipo: TipoHumano}}}
	if err := svc.ValidarDefinicion(def); err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal(def)
	repo.defs["fh"] = flow_model.FlowDef{Id: "fh", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "persona", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51666", "persona", ""); err != nil {
		t.Fatal(err)
	}
	if !avisoPod {
		t.Fatal("debió avisar al pod para marcar la conversación")
	}
	if len(sender.envios) != 1 || !strings.Contains(sender.envios[0].texto, "persona") {
		t.Fatalf("debió enviar el puente, envió %+v", sender.envios)
	}
	if run, _ := repo.RunActivo(ctx, "inst1", "51666"); run != nil {
		t.Fatal("derivado: el run no debe seguir en curso")
	}
	// El siguiente mensaje ya no lo consume el motor: lo ve el pod.
	ok, _ := svc.Evaluar(ctx, inst, "51666", "sigues ahí?", "")
	if ok {
		t.Fatal("tras derivar, el motor debe soltar la conversación")
	}
}

func TestValidacionFase3NuevosTipos(t *testing.T) {
	svc := NewFlowService(&repoFalso{}, &senderFalso{}, nil)
	ok := Definicion{Inicio: "a", Pasos: []Paso{
		{Clave: "a", Tipo: TipoIA, Prompt: "Resume", VariableSalida: "r", MaxTokens: 500, Siguiente: "b"},
		{Clave: "b", Tipo: TipoCorreo, Para: "{{email}}", Asunto: "A", Cuerpo: "C", Siguiente: "c"},
		{Clave: "c", Tipo: TipoReporte, ReporteID: "ventas_hoy", Formato: "texto", Siguiente: "d"},
		{Clave: "d", Tipo: TipoWebhook, URL: "https://n8n.local/hook", Siguiente: "e"},
		{Clave: "e", Tipo: TipoHumano},
	}}
	if err := svc.ValidarDefinicion(ok); err != nil {
		t.Fatalf("combinación válida: %v", err)
	}
	malos := []Paso{
		{Clave: "x", Tipo: TipoIA},
		{Clave: "x", Tipo: TipoCorreo, Para: "sin-arroba", Asunto: "A", Cuerpo: "C"},
		{Clave: "x", Tipo: TipoReporte, ReporteID: "invetado"},
		{Clave: "x", Tipo: TipoWebhook, URL: "ftp://x"},
	}
	for _, m := range malos {
		d := Definicion{Inicio: "x", Pasos: []Paso{m}}
		if err := svc.ValidarDefinicion(d); err == nil {
			t.Fatalf("debió rechazar %+v", m)
		}
	}
}
