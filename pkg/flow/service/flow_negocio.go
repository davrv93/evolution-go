package flow_service

// Pasos de negocio (Fase 3): ia_gemini, correo, reporte, webhook, humano,
// pedido. Los cinco primeros con dato del pod van por Callback; webhook sale
// directo a n8n o a la URL del paso.
//
// Regla de oro: un paso de efecto no reintenta. Si el callback falla, la
// conversación SIGUE (el error queda en el log vía Evaluar): reintentar un
// correo o un pedido es duplicarlo.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

const (
	TipoIA      = "ia_gemini"
	TipoCorreo  = "correo"
	TipoReporte = "reporte"
	TipoWebhook = "webhook"
	TipoHumano  = "humano"
	TipoPedido  = "pedido"
)

// Reportes que el pod sabe armar (lista cerrada: lo que no está, no existe).
var reportesConocidos = map[string]bool{"ventas_hoy": true, "cuentas_por_pagar": true}

// Callback resuelve un paso de negocio en el pod: POST {base}/{accion} con
// {flow_id, run_id, instancia, remitente, paso, contexto} y cabecera
// X-Flow-Secret. Responde {texto?, contexto?} o un error.
type Callback func(ctx context.Context, accion string, payload map[string]any) (map[string]any, error)

// CallbackHTTP arma el Callback contra el pod. Sin URL base, devuelve nil:
// el motor degrada esos pasos en vez de romper la conversación.
func CallbackHTTP(baseURL, secreto string, timeout time.Duration) Callback {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	cli := &http.Client{Timeout: timeout + 2*time.Second}
	return func(ctx context.Context, accion string, payload map[string]any) (map[string]any, error) {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		ctx, cancelar := context.WithTimeout(ctx, timeout)
		defer cancelar()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+accion, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if secreto != "" {
			req.Header.Set("X-Flow-Secret", secreto)
		}
		res, err := cli.Do(req)
		if err != nil {
			return nil, fmt.Errorf("callback %s: %v", accion, err)
		}
		defer res.Body.Close()
		crudo, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		var dec map[string]any
		_ = json.Unmarshal(crudo, &dec)
		if res.StatusCode < 200 || res.StatusCode > 299 {
			msg := ""
			if dec != nil {
				msg, _ = dec["error"].(string)
				if msg == "" {
					msg, _ = dec["message"].(string)
				}
			}
			if msg == "" {
				msg = strings.TrimSpace(string(crudo))
			}
			return nil, fmt.Errorf("callback %s: estado %d: %s", accion, res.StatusCode, recortar(msg, 200))
		}
		if dec == nil {
			dec = map[string]any{}
		}
		return dec, nil
	}
}

func recortar(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var rePlantilla = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

// plantilla sustituye {{variable}} con el contexto. Lo ausente queda vacío:
// mejor un hueco visible que una llave en el WhatsApp del cliente.
func plantilla(s string, vars map[string]string) string {
	return rePlantilla.ReplaceAllStringFunc(s, func(m string) string {
		return vars[rePlantilla.FindStringSubmatch(m)[1]]
	})
}

func textoDe(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	t, _ := resp["texto"].(string)
	return strings.TrimSpace(t)
}

func fusionarContexto(vars map[string]string, resp map[string]any) map[string]string {
	extra, _ := resp["contexto"].(map[string]any)
	for k, v := range extra {
		if s, ok := v.(string); ok {
			vars[k] = s
		}
	}
	return vars
}

// ejecutarNegocio resuelve un paso de negocio y deja el run avanzado o
// cerrado. Nunca deja el run esperando en el mismo paso: así no hay
// reintentos que dupliquen correos o pedidos.
func (s *flowService) ejecutarNegocio(ctx context.Context, inst *instance_model.Instance, run *flow_model.FlowRun, paso *Paso) error {
	vars := mapaContexto(run.Contexto)
	ctxVars := vars

	avanzar := func(siguiente string) error {
		run.Contexto = guardarContexto(ctxVars)
		run.StepActual = siguiente
		if siguiente == "" {
			run.Estado = flow_model.RunCompletado
			s.bitacora("fin flujo %s para %s (negocio %s)", run.FlowID, run.Remitente, paso.Tipo)
			return s.repo.MarcarRun(ctx, run.Id, flow_model.RunCompletado)
		}
		tocarRun(run)
		return s.repo.GuardarRun(ctx, run)
	}
	cerrarComo := func(estado string) error {
		run.Contexto = guardarContexto(ctxVars)
		run.Estado = estado
		s.bitacora("run %s derivado (%s) para %s", run.Id, paso.Tipo, run.Remitente)
		return s.repo.MarcarRun(ctx, run.Id, estado)
	}

	switch paso.Tipo {
	case TipoIA:
		{
			texto := "Ahora mismo no puedo consultar a la IA, sigamos."
			if s.cb != nil {
				payload := map[string]any{
					"sistema":    plantilla(paso.PromptSistema, vars),
					"prompt":     plantilla(paso.Prompt, vars),
					"max_tokens": paso.MaxTokens,
					"flow_id":    run.FlowID,
					"run_id":     run.Id,
					"instancia":  run.InstanceID,
					"remitente":  run.Remitente,
					"paso":       paso.Clave,
					"contexto":   vars,
				}
				if resp, err := s.cb(ctx, "ia", payload); err == nil {
					if t := textoDe(resp); t != "" {
						texto = t
					}
					ctxVars = fusionarContexto(vars, resp)
				}
			}
			if paso.VariableSalida != "" {
				ctxVars[paso.VariableSalida] = texto
			}
			if err := s.sender.Texto(inst, run.Remitente, texto); err != nil {
				return err
			}
			return avanzar(paso.Siguiente)
		}
	case TipoCorreo:
		{
			var primero error
			if s.cb != nil {
				_, primero = s.cb(ctx, "correo", map[string]any{
					"para":      plantilla(paso.Para, vars),
					"asunto":    plantilla(paso.Asunto, vars),
					"cuerpo":    plantilla(paso.Cuerpo, vars),
					"flow_id":   run.FlowID,
					"run_id":    run.Id,
					"instancia": run.InstanceID,
				})
			}
			// Se avanza igual falle o no: reintentar un correo es duplicarlo.
			// El error queda en el log vía Evaluar.
			if err := avanzar(paso.Siguiente); err != nil {
				return err
			}
			return primero
		}
	case TipoReporte:
		{
			texto := "No pude armar el reporte ahora mismo."
			if s.cb != nil {
				if resp, err := s.cb(ctx, "reporte", map[string]any{
					"reporte_id": paso.ReporteID,
					"formato":    formatoReporte(paso),
					"flow_id":    run.FlowID,
					"run_id":     run.Id,
					"instancia":  run.InstanceID,
					"remitente":  run.Remitente,
					"contexto":   vars,
				}); err == nil {
					if t := textoDe(resp); t != "" {
						texto = t
					}
					ctxVars = fusionarContexto(vars, resp)
				}
			}
			if err := s.sender.Texto(inst, run.Remitente, texto); err != nil {
				return err
			}
			return avanzar(paso.Siguiente)
		}
	case TipoWebhook:
		{
			texto, extra := s.llamarWebhook(ctx, run, paso, vars)
			ctxVars = fusionarContexto(vars, extra)
			if texto != "" {
				if err := s.sender.Texto(inst, run.Remitente, texto); err != nil {
					return err
				}
			}
			return avanzar(paso.Siguiente)
		}
	case TipoHumano, TipoPedido:
		{
			puente := "Te comunico con una persona del equipo. Enseguida te escribe."
			if paso.Tipo == TipoPedido {
				puente = "Perfecto, te paso con ventas para tomar tu pedido."
			}
			if paso.Mensaje != "" {
				puente = plantilla(paso.Mensaje, vars)
			}
			if s.cb != nil {
				// Best-effort: si el pod no marca la conversación, igual se deriva.
				_, _ = s.cb(ctx, paso.Tipo, map[string]any{
					"remitente": run.Remitente,
					"flow_id":   run.FlowID,
					"run_id":    run.Id,
					"instancia": run.InstanceID,
					"contexto":  vars,
				})
			}
			if err := s.sender.Texto(inst, run.Remitente, puente); err != nil {
				return err
			}
			// Derivado = el motor suelta la conversación: los siguientes mensajes
			// ya no los consume ningún run y los ve el pod (persona o ventas).
			return cerrarComo(flow_model.RunDerivado)
		}
	default:
		return fmt.Errorf("flujo: paso de negocio desconocido %q", paso.Tipo)
	}
}

func formatoReporte(paso *Paso) string {
	if paso.Formato == "" {
		return "texto"
	}
	return paso.Formato
}

func metodoWebhook(paso *Paso) string {
	m := strings.ToUpper(strings.TrimSpace(paso.Metodo))
	if m != http.MethodGet && m != http.MethodPost {
		return http.MethodPost
	}
	return m
}

// llamarWebhook sale directo a n8n o a la URL del paso. Nunca rompe: sin
// respuesta útil, devuelve texto vacío y la conversación sigue su curso.
func (s *flowService) llamarWebhook(ctx context.Context, run *flow_model.FlowRun, paso *Paso, vars map[string]string) (string, map[string]any) {
	metodo := metodoWebhook(paso)
	segs := paso.TimeoutSegs
	if segs < 2 || segs > 30 {
		segs = 8
	}
	ctx, cancelar := context.WithTimeout(ctx, time.Duration(segs)*time.Second)
	defer cancelar()
	cuerpo, _ := json.Marshal(map[string]any{
		"flow_id": run.FlowID, "run_id": run.Id, "instancia": run.InstanceID,
		"remitente": run.Remitente, "paso": paso.Clave, "contexto": vars,
	})
	var req *http.Request
	var err error
	if metodo == http.MethodGet {
		req, err = http.NewRequestWithContext(ctx, metodo, paso.URL, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, metodo, paso.URL, bytes.NewReader(cuerpo))
		req.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		return "", nil
	}
	if strings.TrimSpace(paso.Secreto) != "" {
		req.Header.Set("X-Flow-Secret", strings.TrimSpace(paso.Secreto))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil
	}
	defer res.Body.Close()
	crudo, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", nil
	}
	var dec map[string]any
	if err := json.Unmarshal(crudo, &dec); err != nil || dec == nil {
		return "", nil
	}
	return textoDe(dec), dec
}
