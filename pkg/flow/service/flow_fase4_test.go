package flow_service

// Fase 4: consentimiento/baja (Ley 29733), topes de WhatsApp y bitácora.
// Callback falso: permiso configurable; baja siempre ok.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

func conPermiso(permitir bool, falla bool) Callback {
	return func(_ context.Context, accion string, _ map[string]any) (map[string]any, error) {
		switch accion {
		case "permiso":
			if falla {
				return nil, errors.New("pod caído")
			}
			return map[string]any{"ok": permitir}, nil
		case "baja":
			return map[string]any{"ok": true}, nil
		}
		return map[string]any{}, nil
	}
}

func TestVetoDeBajaNoDispara(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	var bit []string
	svc := NewFlowService(repo, sender, conPermiso(false, false))
	svc.SetLog(func(f string, a ...any) { bit = append(bit, f) })
	def := definicionEncuesta()
	steps, _ := json.Marshal(def)
	repo.defs["f1"] = flow_model.FlowDef{Id: "f1", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ok, err := svc.Evaluar(context.Background(), inst, "51999", "hola", "")
	if err != nil || ok {
		t.Fatalf("con veto no se consume: ok=%v err=%v", ok, err)
	}
	if len(sender.envios) != 0 {
		t.Fatalf("vetado: nada debe enviarse, envió %+v", sender.envios)
	}
	if len(bit) == 0 {
		t.Fatal("el veto debe quedar en bitácora")
	}
}

func TestPermisoConFalloDejaPasarConHuella(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	var bit []string
	svc := NewFlowService(repo, sender, conPermiso(true, true))
	svc.SetLog(func(f string, a ...any) { bit = append(bit, f) })
	def := definicionEncuesta()
	steps, _ := json.Marshal(def)
	repo.defs["f1"] = flow_model.FlowDef{Id: "f1", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ok, err := svc.Evaluar(context.Background(), inst, "51999", "hola", "")
	if ok || err == nil {
		t.Fatalf("fallo de permiso: no se consume y deja huella: ok=%v err=%v", ok, err)
	}
	if len(bit) == 0 {
		t.Fatal("el fallo debe quedar en bitácora")
	}
}

func TestBajaEnMitadDelFlujoCierraYConfirma(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	svc := NewFlowService(repo, sender, conPermiso(true, false))
	def := definicionEncuesta()
	steps, _ := json.Marshal(def)
	repo.defs["f1"] = flow_model.FlowDef{Id: "f1", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51888", "hola", ""); err != nil {
		t.Fatal(err)
	}
	sender.envios = nil
	ok, err := svc.Evaluar(ctx, inst, "51888", "BAJA", "")
	if err != nil || !ok {
		t.Fatalf("la baja se consume: ok=%v err=%v", ok, err)
	}
	if len(sender.envios) != 1 || !strings.Contains(sender.envios[0].texto, "no te escribiremos") {
		t.Fatalf("debió confirmar la baja, envió %+v", sender.envios)
	}
	if run, _ := repo.RunActivo(ctx, "inst1", "51888"); run != nil {
		t.Fatal("tras la baja no debe quedar run en curso")
	}
}

func TestTopesDeWhatsApp(t *testing.T) {
	svc := NewFlowService(&repoFalso{}, &senderFalso{}, nil)
	etiquetaLarga := strings.Repeat("x", 26)
	filas := make([]Fila, 0, 11)
	for i := 0; i < 11; i++ {
		filas = append(filas, Fila{Id: "f", Titulo: "t", IrA: "fin"})
	}
	def := Definicion{Inicio: "a", Pasos: []Paso{
		{Clave: "a", Tipo: TipoBotones, Texto: "E", Respaldo: "R",
			Botones: []Opcion{{Id: "o", Etiqueta: etiquetaLarga, IrA: "fin"}}},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Fin"},
	}}
	if err := svc.ValidarDefinicion(def); err == nil {
		t.Fatal("etiqueta de 26 debe rechazarse")
	}
	def2 := Definicion{Inicio: "l", Pasos: []Paso{
		{Clave: "l", Tipo: TipoLista, Texto: "E", Respaldo: "R", Secciones: []Seccion{{Filas: filas}}},
		{Clave: "fin", Tipo: TipoMensaje, Texto: "Fin"},
	}}
	if err := svc.ValidarDefinicion(def2); err == nil {
		t.Fatal("11 filas deben rechazarse")
	}
}
