package flow_service

// {{variables}} en todo lo que sale al cliente. Antes solo IA, correo y
// humano pasaban por plantilla(): un flujo que guardaba `producto` en una
// espera y luego decía «Reviso si tenemos {{producto}}» mandaba las llaves
// literales. Ahora mensaje, botones, lista, espera y la pregunta de la
// encuesta se expanden con el contexto del run justo antes de enviarse.
//
// Qué NO se expande:
//   - los ids de botón/fila/opción (son la respuesta que vuelve);
//   - las opciones de una encuesta (el voto llega como hash del texto exacto:
//     ver flow_encuesta.go);
//   - la vista previa (VistaPrevia/Probar), que enseña la plantilla tal cual.

// pasoExpandido devuelve una COPIA del paso con los textos visibles
// expandidos. El paso de la definición no se toca: la siguiente conversación
// parte otra vez de la plantilla.
func pasoExpandido(p *Paso, vars map[string]string) Paso {
	q := *p
	q.Texto = plantilla(p.Texto, vars)
	q.Titulo = plantilla(p.Titulo, vars)
	q.Pie = plantilla(p.Pie, vars)
	q.Respaldo = plantilla(p.Respaldo, vars)
	q.MensajeError = plantilla(p.MensajeError, vars)
	q.TextoBoton = plantilla(p.TextoBoton, vars)
	if len(p.Botones) > 0 {
		q.Botones = make([]Opcion, len(p.Botones))
		for i, o := range p.Botones {
			o.Etiqueta = plantilla(o.Etiqueta, vars)
			q.Botones[i] = o
		}
	}
	if len(p.Secciones) > 0 {
		q.Secciones = make([]Seccion, len(p.Secciones))
		for i, sec := range p.Secciones {
			filas := make([]Fila, len(sec.Filas))
			for j, f := range sec.Filas {
				f.Titulo = plantilla(f.Titulo, vars)
				f.Descripcion = plantilla(f.Descripcion, vars)
				filas[j] = f
			}
			q.Secciones[i] = Seccion{Titulo: plantilla(sec.Titulo, vars), Filas: filas}
		}
	}
	return q
}
