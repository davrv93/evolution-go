//go:build !noswagger

package routes

import (
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/evolution-foundation/evolution-go/docs"
)

// registerSwagger sirve la UI y el spec en /swagger/*. Arrastra al binario el
// paquete docs (~0,5 MB de spec), la UI embebida de swaggo/files y las
// dependencias de swag (go/parser y compañía). Se excluye con `-tags noswagger`.
func registerSwagger(eng *gin.Engine) {
	eng.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
}
