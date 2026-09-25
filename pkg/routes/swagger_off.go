//go:build noswagger

package routes

import "github.com/gin-gonic/gin"

// registerSwagger no hace nada en builds con `-tags noswagger` (producción):
// /swagger/* responde 404 y el binario no carga el spec ni la UI.
func registerSwagger(*gin.Engine) {}
