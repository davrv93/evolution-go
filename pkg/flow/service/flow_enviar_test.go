package flow_service

// Envío saliente (encuestas): conteo por resultado y elección guardada.

import (
	"context"
	"encoding/json"
	"testing"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

func TestEnviarCuentaPorResultado(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	cb := func(_ context.Context, accion string, payload map[string]any) (map[string]any, error) {
		if accion == "permiso" {
			rem, _ := payload["remitente"].(string)
			if rem == "51999000" {
				return map[string]any{"ok": false}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		return map[string]any{}, nil
	}
	svc := NewFlowService(repo, sender, cb)
	def := definicionEncuesta()
	steps, _ := json.Marshal(def)
	rec := &flow_model.FlowDef{Id: "fe", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	repo.defs["fe"] = *rec
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()

	cuenta := svc.Enviar(ctx, inst, rec,
		[]string{"+51 999 111", "51999000", "corto", "+51 999 111", "51999222"}, 0)
	if cuenta["enviados"] != 2 || cuenta["vetados"] != 1 || cuenta["invalidos"] != 2 {
		t.Fatalf("conteo mal: %+v", cuenta)
	}
	if len(sender.envios) == 0 {
		t.Fatal("debió enviar bienvenidas")
	}

	// Reenviar al mismo: ya tienen run en curso.
	cuenta2 := svc.Enviar(ctx, inst, rec, []string{"51999111", "51999222"}, 0)
	if cuenta2["activos"] != 2 || cuenta2["enviados"] != 0 {
		t.Fatalf("debió saltar activos: %+v", cuenta2)
	}
}

func TestOpcionElegidaQuedaEnContexto(t *testing.T) {
	repo := &repoFalso{defs: map[string]flow_model.FlowDef{}, runs: map[string]*flow_model.FlowRun{}}
	sender := &senderFalso{}
	cb := func(_ context.Context, accion string, _ map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}
	svc := NewFlowService(repo, sender, cb)
	def := definicionEncuesta()
	steps, _ := json.Marshal(def)
	repo.defs["f1"] = flow_model.FlowDef{Id: "f1", InstanceID: "inst1", Estado: flow_model.EstadoActivo, Entrada: "hola", Steps: steps}
	inst := &instance_model.Instance{Id: "inst1"}
	ctx := context.Background()
	if _, err := svc.Evaluar(ctx, inst, "51999", "hola", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Evaluar(ctx, inst, "51999", "", "stock"); err != nil {
		t.Fatal(err)
	}
	run := repo.runs["run-51999"]
	vars := mapaContexto(run.Contexto)
	if vars["opcion_pregunta"] != "stock" {
		t.Fatalf("la elección debió guardarse, contexto: %+v", vars)
	}
}

func TestNumeroLimpio(t *testing.T) {
	if numeroLimpio("+51 999 888 777") != "51999888777" {
		t.Fatal("limpia espacios y +")
	}
	if numeroLimpio("123") != "" || numeroLimpio("abcdefghi") != "" {
		t.Fatal("corto o sin dígitos se rechaza")
	}
	if numeroLimpio("1234567890123456") != "" {
		t.Fatal("más de 15 se rechaza")
	}
}
