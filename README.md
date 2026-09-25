# Evolution Go — fork de PjgFactSalud

Este repositorio contiene un fork de Evolution Go mantenido para integrar
WhatsApp con PjgFactSalud. Conserva el motor Go y `whatsmeow` como base, y añade
ajustes de operación, memoria y compatibilidad con el backend Laravel.

Este fork es independiente; no es una distribución oficial ni implica soporte
de Evolution Foundation. Código y avisos de copyright upstream se conservan en
[`LICENSE`](./LICENSE), [`NOTICE`](./NOTICE) y [`TRADEMARKS.md`](./TRADEMARKS.md).

## Qué aporta el fork

- Elimina la activación de licencia, sus rutas y el heartbeat de telemetría del
  proceso. El servicio no necesita contactar un servidor de licencias.
- Mantiene autenticación administrativa para gestionar instancias y token
  individual por instancia para operaciones WhatsApp.
- Conecta mensajes entrantes al webhook de Laravel. Laravel conserva menús
  conversacionales, notificaciones, cola de mensajes y funciones de negocio.
- Desactiva almacenamiento duplicado de mensajes y adjuntos en Evolution Go;
  PjgFactSalud conserva sus mensajes en `whatsapp_inbox`.
- Limita pools PostgreSQL y cachés temporales, desactiva sincronización completa
  de historial y devuelve memoria libre al sistema después de picos.

## Perfil de recursos de PjgFactSalud

Valores configurados en el Compose de producción:

- `GOMEMLIMIT=100MiB`; límite cgroup del contenedor `128MiB`.
- Pools de PostgreSQL: hasta 10 conexiones abiertas y 2 ociosas por pool. Todos
  los dispositivos comparten un `sqlstore.Container` y pool de autenticación.
- Caché LID de contactos: máximo 4.096 pares. Caché de recibos duplicados:
  máximo 10.000 claves durante 10 minutos.
- `DATABASE_SAVE_MESSAGES=false`, `WEBHOOK_FILES=false` y `RequireFullSync=false`.
- `debug.FreeOSMemory()` cada 5 minutos. Libera páginas que ya no contienen
  objetos vivos; no reduce memoria que el proceso todavía necesita.
- Recolector: si el entorno no trae `GOGC` / `GOMEMLIMIT`, el binario aplica
  `GOGC=50` y `GOMEMLIMIT=96MiB` (heap a ~1,5x lo vivo en vez de 2x). Gin
  arranca en `release` salvo que `GIN_MODE` diga otra cosa.
- `HISTORY_SYNC_DOWNLOAD=false` (fábrica): whatsmeow acusa recibo del
  historial que manda el teléfono pero **no lo descarga ni decodifica**. Nadie
  lo consumía y cada chunk podía pesar decenas de MB.
- `WEBHOOK_TIMEOUT_SECONDS=90` (fábrica): tope por POST saliente, cliente HTTP
  compartido y respuesta leída hasta 64 KB. Antes, un receptor colgado retenía
  goroutine, socket y payload indefinidamente por evento y reintento.
- `/message/downloadmedia` codifica el base64 directamente sobre la respuesta:
  el pico pasa de ~4x el tamaño del medio a ~1x. Mismo JSON byte a byte.
- Imagen: `-trimpath -ldflags "-s -w" -tags noswagger` (binario de 64,7 MB a
  35,1 MB). Swagger vuelve con `--build-arg GO_TAGS=""`.

### Mediciones

Medición local del 25-09-2026, cuatro estados del código con el mismo método.

| Estado | Binario | RSS reposo 60 s | RSS tras carga | RSS 20 s después |
|---|---:|---:|---:|---:|
| Antes de todo (`8d6cf06`) | 64,6 MB | 26,9 MB | 54,2 MB | 27,4 MB |
| Tras límites de memoria y Postgres (`7e6dd48`) | 64,7 MB | 26,9 MB | 57,9 MB | 27,5 MB |
| Ahora, mismos flags de build | 64,6 MB | 27,2 MB | 51,4 MB | 27,9 MB |
| Ahora, Dockerfile nuevo (`noswagger`, `-s -w`) | 35,1 MB | **18,1 MB** | 31,6 MB | 17,9 MB |

Método: macOS arm64, binario compilado con CGO y los flags de build de cada
estado, Postgres 16 efímero en Docker, `GOMEMLIMIT=100MiB` como en producción,
**cero instancias** (sin sesión de WhatsApp). RSS = `ps -o rss` (KB ÷ 1000) a
los 60 s de arranque; «carga» = 300× `GET /instance/all` + 20×
`/swagger/doc.json` (404 en el binario `noswagger`); tercera lectura 20 s
después. La bajada en reposo la explica quitar Swagger (−9,1 MB medidos
aislando la variable); `-s -w` solo encoge el binario. El efecto de `GOGC=50`
y de no descargar el HistorySync **solo aparece con una sesión vinculada**, que
aquí no hay: el heap vivo en reposo sin sesión es demasiado pequeño para verlo.

<!-- PROD -->

## Tres métricas de producción

Medición en producción del 25-09-2026, con una sesión WhatsApp conectada. No es
benchmark; son lecturas de `docker stats`, cgroup y `pg_stat_activity`.

| Métrica | Antes del ajuste | Después del ajuste |
|---|---:|---:|
| RAM actual del contenedor Go | 93,4 MiB | 76,4 MiB (**−18 %**) |
| Pico de memoria cgroup | 97,9 MiB | 80,3 MiB de 128 MiB |
| Conexiones PostgreSQL ociosas | 5 (`auth` 4 + `users` 1) | 3 (`auth` 2 + `users` 1) |

## Compilar y probar

Requiere Go 1.25 o posterior y PostgreSQL.

```bash
go test ./...
go vet ./...
go build ./cmd/evolution-go
```

Swagger está disponible en `/swagger/index.html` cuando el servicio se compila
sin la etiqueta `noswagger` (la imagen de producción la lleva puesta).
El despliegue de PjgFactSalud se coordina en el
[repositorio de la aplicación](https://github.com/davrv93/pjgfarma).

## Upstream y licencia

Este fork parte de
[evolution-foundation/evolution-go](https://github.com/evolution-foundation/evolution-go)
y usa [whatsmeow](https://github.com/tulir/whatsmeow) para el protocolo
WhatsApp. Consulta `LICENSE`, `NOTICE` y `TRADEMARKS.md` para términos y
atribuciones completas.
