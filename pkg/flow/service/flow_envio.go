package flow_service

// Envío saliente (encuestas y cualquier flujo activo a una lista).
//
// Dos mitades:
//   - preparar (síncrona, rápida): limpia números, descarta duplicados, salta
//     a quien ya tiene el flujo en curso y pregunta el permiso al pod (Ley
//     29733). No envía nada.
//   - despachar (lenta): inicia el flujo número por número con pausa entre
//     envíos y avisa al pod de cada resultado (callback «envio») para su
//     bitácora.
//
// El handler usa Programar: prepara, contesta YA con el conteo y despacha
// en segundo plano. Con 200 números y 1 s de pausa son más de 3 minutos, y el
// pod (20 s) y Laravel (25 s) cortan antes: el envío síncrono de antes se
// quedaba a medias en cuanto el cliente HTTP colgaba.

import (
	"context"
	"encoding/json"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

// Resultados por número (detalle y callback «envio»).
const (
	EnvioProgramado = "programado"
	EnvioEnviado    = "enviado"
	EnvioVetado     = "vetado"
	EnvioActivo     = "activo"
	EnvioInvalido   = "invalido"
	EnvioFallo      = "fallo"
)

type DetalleEnvio struct {
	Numero    string `json:"numero"`
	Resultado string `json:"resultado"`
	Error     string `json:"error,omitempty"`
}

// reservar marca un número como «en envío» para esta instancia. Dos envíos
// simultáneos a la misma lista no mandan dos veces: el segundo lo ve activo.
func (s *flowService) reservar(inst, numero string) bool {
	s.muEnvio.Lock()
	defer s.muEnvio.Unlock()
	if s.enEnvio == nil {
		s.enEnvio = map[string]bool{}
	}
	k := inst + "|" + numero
	if s.enEnvio[k] {
		return false
	}
	s.enEnvio[k] = true
	return true
}

func (s *flowService) liberar(inst, numero string) {
	s.muEnvio.Lock()
	delete(s.enEnvio, inst+"|"+numero)
	s.muEnvio.Unlock()
}

// avisarEnvio deja constancia en el pod (bitácora). Nunca rompe el envío.
func (s *flowService) avisarEnvio(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, def *Definicion, numero, resultado, motivo string) {
	if s.cb == nil {
		return
	}
	resumen := ""
	if def != nil {
		if p := buscarPaso(def, def.Inicio); p != nil {
			resumen = p.Texto
		}
	}
	if _, err := s.cb(ctx, "envio", map[string]any{
		"instancia": inst.Id, "remitente": numero, "flow_id": d.Id, "flujo": d.Nombre,
		"resultado": resultado, "error": motivo, "resumen": resumen,
	}); err != nil {
		s.bitacora("envío: bitácora del pod sin respuesta para %s: %v", numero, err)
	}
}

func nuevaCuenta() map[string]int {
	return map[string]int{"enviados": 0, "programados": 0, "vetados": 0, "activos": 0, "invalidos": 0, "fallos": 0}
}

// preparar deja la lista de aptos (ya reservados) y el conteo de descartes.
func (s *flowService) preparar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, def *Definicion, remitentes []string) ([]string, map[string]int, []DetalleEnvio) {
	cuenta := nuevaCuenta()
	detalle := make([]DetalleEnvio, 0, len(remitentes))
	var aptos []string
	vistos := map[string]bool{}
	for _, crudo := range remitentes {
		numero := numeroLimpio(crudo)
		if numero == "" || vistos[numero] {
			cuenta["invalidos"]++
			detalle = append(detalle, DetalleEnvio{Numero: crudo, Resultado: EnvioInvalido})
			continue
		}
		vistos[numero] = true
		if run, err := s.repo.RunActivo(ctx, inst.Id, numero); err == nil && run != nil {
			// Un run viejo que nadie cerró (el cliente no volvió a escribir)
			// no bloquea para siempre: vencido, se abandona y se envía.
			if !s.vencido(ctx, inst, run) {
				cuenta["activos"]++
				detalle = append(detalle, DetalleEnvio{Numero: numero, Resultado: EnvioActivo})
				continue
			}
			_ = s.repo.MarcarRun(ctx, run.Id, flow_model.RunAbandonado)
		}
		if s.cb != nil {
			resp, err := s.cb(ctx, "permiso", map[string]any{"instancia": inst.Id, "remitente": numero})
			if err != nil || resp == nil {
				s.bitacora("envío: permiso sin respuesta para %s", numero)
				cuenta["fallos"]++
				detalle = append(detalle, DetalleEnvio{Numero: numero, Resultado: EnvioFallo, Error: "sin respuesta del permiso"})
				continue
			}
			if ok, _ := resp["ok"].(bool); !ok {
				cuenta["vetados"]++
				detalle = append(detalle, DetalleEnvio{Numero: numero, Resultado: EnvioVetado})
				s.avisarEnvio(ctx, inst, d, def, numero, EnvioVetado, "baja (Ley 29733)")
				continue
			}
		}
		if !s.reservar(inst.Id, numero) {
			cuenta["activos"]++
			detalle = append(detalle, DetalleEnvio{Numero: numero, Resultado: EnvioActivo})
			continue
		}
		aptos = append(aptos, numero)
	}
	return aptos, cuenta, detalle
}

// despachar inicia el flujo para cada apto y libera su reserva.
func (s *flowService) despachar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, def *Definicion, aptos []string, pausa time.Duration, cuenta map[string]int) {
	defer func() {
		for _, n := range aptos {
			s.liberar(inst.Id, n)
		}
	}()
	for i, numero := range aptos {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.iniciar(ctx, inst, d, def, numero); err != nil {
			cuenta["fallos"]++
			s.bitacora("envío flujo %s a %s: %v", d.Id, numero, err)
			s.avisarEnvio(ctx, inst, d, def, numero, EnvioFallo, recortar(err.Error(), 200))
		} else {
			cuenta["enviados"]++
			s.avisarEnvio(ctx, inst, d, def, numero, EnvioEnviado, "")
		}
		if pausa > 0 && i < len(aptos)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pausa):
			}
		}
	}
	s.bitacora("envío flujo %s: %+v", d.Id, cuenta)
}

// Programar prepara en el acto y despacha en segundo plano. El conteo que
// devuelve lleva «programados» (los que van a salir); enviados/fallos reales
// quedan en la bitácora del pod y en los runs.
func (s *flowService) Programar(ctx context.Context, inst *instance_model.Instance, d *flow_model.FlowDef, remitentes []string, pausa time.Duration) (map[string]int, []DetalleEnvio) {
	var def Definicion
	if err := json.Unmarshal(d.Steps, &def); err != nil {
		cuenta := nuevaCuenta()
		cuenta["fallos"] = len(remitentes)
		return cuenta, nil
	}
	aptos, cuenta, detalle := s.preparar(ctx, inst, d, &def, remitentes)
	cuenta["programados"] = len(aptos)
	for _, n := range aptos {
		detalle = append(detalle, DetalleEnvio{Numero: n, Resultado: EnvioProgramado})
	}
	if len(aptos) > 0 {
		// Contexto propio: el envío sigue aunque el que pidió cuelgue. Tope
		// de una hora por si algo se traba (200 × 10 s de pausa máx. = 33 min).
		fondo, cancelar := context.WithTimeout(context.Background(), time.Hour)
		propia := *d
		cuentaFondo := map[string]int{"enviados": 0, "fallos": 0}
		go func() {
			defer cancelar()
			s.despachar(fondo, inst, &propia, &def, aptos, pausa, cuentaFondo)
		}()
	}
	return cuenta, detalle
}
