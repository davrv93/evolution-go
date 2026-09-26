package flow_handler

import (
	"net/http"
	"time"

	flow_model "github.com/evolution-foundation/evolution-go/pkg/flow/model"
	flow_repository "github.com/evolution-foundation/evolution-go/pkg/flow/repository"
	flow_service "github.com/evolution-foundation/evolution-go/pkg/flow/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

type FlowHandler interface {
	Listar(ctx *gin.Context)
	Crear(ctx *gin.Context)
	Ver(ctx *gin.Context)
	Actualizar(ctx *gin.Context)
	Eliminar(ctx *gin.Context)
	Activar(ctx *gin.Context)
	Pausar(ctx *gin.Context)
	Probar(ctx *gin.Context)
	Runs(ctx *gin.Context)
	Enviar(ctx *gin.Context)
	Resultados(ctx *gin.Context)
}

type flowHandler struct {
	svc  flow_service.FlowService
	repo flow_repository.FlowRepository
}

func NewFlowHandler(svc flow_service.FlowService, repo flow_repository.FlowRepository) FlowHandler {
	return &flowHandler{svc: svc, repo: repo}
}

func instanciaDe(ctx *gin.Context) (*instance_model.Instance, bool) {
	inst, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok || inst == nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return nil, false
	}
	return inst, true
}

type definicionEntrada struct {
	Nombre      string                  `json:"nombre"`
	Descripcion string                  `json:"descripcion"`
	Entrada     string                  `json:"entrada"`
	Estado      string                  `json:"estado"`
	Def         flow_service.Definicion `json:"def"`
	// Opcionales (flow_handler_menu.go): ausentes = no se tocan, así el
	// constructor que no los conoce no borra la marca de un menú.
	Audiencia  *string `json:"audiencia"`
	PorDefecto *bool   `json:"porDefecto"`
}

func (h *flowHandler) Listar(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	defs, err := h.repo.DefsPorInstancia(ctx.Request.Context(), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": defs})
}

func (h *flowHandler) Crear(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	var in definicionEntrada
	if err := ctx.ShouldBindBodyWithJSON(&in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if in.Nombre == "" || in.Entrada == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "nombre y entrada son obligatorios"})
		return
	}
	if err := h.svc.ValidarDefinicion(in.Def); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	def := &flow_model.FlowDef{
		InstanceID: inst.Id, Nombre: in.Nombre, Descripcion: in.Descripcion,
		Estado: flow_model.EstadoBorrador, Entrada: in.Entrada,
	}
	def.Steps = pasosJSON(in.Def)
	if err := aplicarAudiencia(def, in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.repo.CrearDef(ctx.Request.Context(), def); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": def})
}

func (h *flowHandler) Ver(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	def, err := h.repo.DefPorID(ctx.Request.Context(), ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": def})
}

func (h *flowHandler) Actualizar(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	c := ctx.Request.Context()
	def, err := h.repo.DefPorID(c, ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	var in definicionEntrada
	if err := ctx.ShouldBindBodyWithJSON(&in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.ValidarDefinicion(in.Def); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	def.Nombre = in.Nombre
	def.Descripcion = in.Descripcion
	def.Entrada = in.Entrada
	def.Steps = pasosJSON(in.Def)
	if err := aplicarAudiencia(def, in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.repo.ActualizarDef(c, def); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": def})
}

func (h *flowHandler) Eliminar(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	if err := h.repo.EliminarDef(ctx.Request.Context(), ctx.Param("id"), inst.Id); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success"})
}

func (h *flowHandler) cambiarEstado(ctx *gin.Context, estado string) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	c := ctx.Request.Context()
	def, err := h.repo.DefPorID(c, ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	if estado == flow_model.EstadoActivo {
		var d flow_service.Definicion
		if err := defSteps(def, &d); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "definición corrupta, no se puede activar"})
			return
		}
		if err := h.svc.ValidarDefinicion(d); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if estado == flow_model.EstadoActivo && def.PorDefecto {
		h.pausarOtrosPorDefecto(c, def) // un solo menú activo por audiencia
	}
	def.Estado = estado
	if err := h.repo.ActualizarDef(c, def); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": def})
}

func (h *flowHandler) Activar(ctx *gin.Context) { h.cambiarEstado(ctx, flow_model.EstadoActivo) }
func (h *flowHandler) Pausar(ctx *gin.Context)  { h.cambiarEstado(ctx, flow_model.EstadoPausado) }

// Probar valida y devuelve la vista previa sin guardar ni enviar nada.
func (h *flowHandler) Probar(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	var def flow_service.Definicion
	if id := ctx.Param("id"); id != "" {
		rec, err := h.repo.DefPorID(ctx.Request.Context(), id, inst.Id)
		if err != nil {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
			return
		}
		if err := defSteps(rec, &def); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "definición corrupta"})
			return
		}
	} else if err := ctx.ShouldBindBodyWithJSON(&def); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	vista, err := h.svc.VistaPrevia(def)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": vista})
}

func (h *flowHandler) Runs(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	rec, err := h.repo.DefPorID(ctx.Request.Context(), ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	runs, err := h.repo.RunsPorFlow(ctx.Request.Context(), rec.Id, ctx.Query("estado"), 0)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": runs})
}

type envioEntrada struct {
	Remitentes []string `json:"remitentes"`
	// PausaMs entre envíos (fábrica 1000): no castigar la línea con ráfagas.
	PausaMs int `json:"pausa_ms"`
}

// Enviar dispara el flujo a una lista (encuesta saliente). Solo flujos
// activos, máx. 200 remitentes por llamada. Contesta en el acto con el
// conteo (vetados, activos, inválidos, programados) y el detalle por número;
// los envíos salen en segundo plano con la pausa pedida (Programar).
func (h *flowHandler) Enviar(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	c := ctx.Request.Context()
	def, err := h.repo.DefPorID(c, ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	if def.Estado != flow_model.EstadoActivo {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "solo se puede enviar un flujo activo"})
		return
	}
	var in envioEntrada
	if err := ctx.ShouldBindBodyWithJSON(&in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(in.Remitentes) == 0 || len(in.Remitentes) > 200 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "de 1 a 200 remitentes por envío"})
		return
	}
	pausa := time.Second
	if in.PausaMs > 0 && in.PausaMs <= 10000 {
		pausa = time.Duration(in.PausaMs) * time.Millisecond
	}
	cuenta, detalle := h.svc.Programar(c, inst, def, in.Remitentes, pausa)
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": cuenta, "detalle": detalle,
		"flujo": gin.H{"id": def.Id, "nombre": def.Nombre}})
}

// Resultados agrega las respuestas de cada encuesta/botones/lista del flujo:
// votos por opción y quién votó (flow_service.Resultados). Solo lectura.
func (h *flowHandler) Resultados(ctx *gin.Context) {
	inst, ok := instanciaDe(ctx)
	if !ok {
		return
	}
	c := ctx.Request.Context()
	rec, err := h.repo.DefPorID(c, ctx.Param("id"), inst.Id)
	if err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "flujo no encontrado"})
		return
	}
	var def flow_service.Definicion
	if err := defSteps(rec, &def); err != nil {
		ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": "definición ilegible"})
		return
	}
	runs, err := h.repo.RunsParaResultados(c, rec.Id, 0)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": gin.H{
		"flujo": gin.H{"id": rec.Id, "nombre": rec.Nombre, "estado": rec.Estado},
		"runs":  len(runs), "preguntas": flow_service.Resultados(def, runs),
	}})
}
