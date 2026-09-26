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
- Imagen: `-trimpath -ldflags "-s -w"` y las etiquetas
  `noswagger nosqlite nomsgpack nominio nonats noamqp` (binario de 64,7 MB a
  35,1 MB con `noswagger`, y a 27,8 MB con el resto). Cada etiqueta quita del
  binario algo que esta instalación no usa **y** el heap que su `init()`
  reservaba al arrancar aunque estuviera apagado:
  - `nosqlite`: driver SQLite (modernc, libc transpilada). La sesión va a
    `POSTGRES_AUTH_DB`, que pasa a ser obligatorio.
  - `nomsgpack`: códec msgpack de gin (etiqueta oficial de gin).
  - `nominio`: cliente MinIO/S3. Arrastraba `goccy/go-json`, cuyo `init()`
    reservaba ~2 MB de cachés de tipos en el heap de Linux: la mayor partida
    del heap vivo en reposo.
  - `nonats` / `noamqp`: clientes NATS y RabbitMQ.

  Si se enciende una de esas funciones (`MINIO_ENABLED=true`, `NATS_URL`,
  `AMQP_URL`, o `POSTGRES_AUTH_DB` vacío) con este binario, **el arranque
  falla** con un mensaje que dice qué etiqueta quitar; nunca pierde eventos en
  silencio. Todo vuelve con `--build-arg GO_TAGS=""` (o `go build` a secas).
- Cachés de mensajes procesados y de LID sin capacidad reservada: se reservaban
  10.000 y 2×4.096 huecos al arrancar (~1,2 MB de heap vivo con cero
  mensajes). Crecen al usarse; el tope sigue siendo el mismo.
- Sin `NATS_URL` ya no se intenta conectar: `nats.Connect("")` significaba
  `nats://127.0.0.1:4222`, no «apagado» (de ahí el `Failed to connect to NATS`
  de cada arranque).

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

Segunda tanda, 25-09-2026 por la noche, mismo método y mismo Mac; se vuelve a
medir el estado anterior porque el Mac tenía otra presión de memoria:

| Estado | Binario | RSS reposo 60 s | RSS tras carga | RSS 20 s después | Physical footprint |
|---|---:|---:|---:|---:|---:|
| Antes de esta tanda (`bf63c71`, `noswagger`) | 35,1 MB | 16,8 MB | 31,1 MB | 17,8 MB | 15,2 MB |
| Etiquetas `nosqlite nomsgpack nominio nonats noamqp` + cachés sin reserva | 27,8 MB | **15,0 MB** | 25,4 MB | 16,1 MB | **10,8 MB** |

En macOS el `ps -o rss` de un proceso ocioso es sobre todo **binario mapeado**:
el sistema comprime las páginas anónimas (heap) y el RSS que queda son páginas
de `__TEXT` (~10 MB tocadas de 23 MB) y `__DATA_CONST` (3,7 MB, residente
entero porque dyld reubica cada puntero de los descriptores de tipos). Por eso
aquí baja poco y el `Physical footprint` de `vmmap -summary` (memoria sucia
propia, comprimida o no) es la cifra que refleja el heap.

**Linux, imagen real** (Dockerfile, Go 1.25.0, alpine, `--cpuset-cpus 0,1`
como el EC2 de 2 vCPU, `--memory 128m`, Postgres efímero, 0 instancias,
lectura a los 60 s; tres rondas por estado):

| Estado | Binario | `docker stats` | `memory.current` | RssAnon | VmRSS | Heap vivo (`HeapAlloc`) |
|---|---:|---:|---:|---:|---:|---:|
| Antes (`bf63c71`) | 28,3 MB | 8,3–8,6 MiB | 8,8–9,2 MiB | 7,4 MB | 27,8 MB | 6,0 MB |
| Esta versión | 22,1 MB | **6,5–6,6 MiB** | 6,9–7,2 MiB | 5,8 MB | 22,9 MB | 2,7 MB |
| Esta versión, caché de páginas fría | 22,1 MB | 27,1–27,7 MiB | — | — | 24,7–25,3 MB | — |
| Antes, caché de páginas fría | 28,3 MB | 32,8–35,6 MiB | — | — | 29,8–32,2 MB | — |

`docker stats` es la memoria anónima del cgroup más la caché de páginas
**que se le haya cobrado**. Normalmente el binario lo leyó quien bajó o
construyó la imagen y no se cobra al contenedor (filas 1 y 2). Si la caché se
vacía (reinicio del servidor, presión de memoria) el contenedor relee el
binario y se lo cobra: filas «fría», medidas vaciando la caché de la VM de
Docker antes de arrancar. Ahí el tamaño del binario sí cuenta en producción.

Qué cambió, medido aislando cada paso (Linux, `docker stats` en reposo):
cachés sin reserva −1,2 MB de anónima; `nosqlite` −0,1 MiB de anónima y −1 MB de
binario mapeado (en macOS eran 1,7 MB de heap: netdb de la libc transpilada);
`nomsgpack` −0,7 MB de binario; `nominio` −0,4 MiB de anónima (los ~2 MB que
`goccy/go-json` reservaba casi no se tocaban, así que pesaban poco en RSS) y
−2,1 MB de binario; `nonats` + `noamqp` −1,1 MB de binario.
`debug.FreeOSMemory()` al arrancar no gana nada (el scavenger ya
devolvió el 97 % del heap ocioso) y `GOMEMLIMIT=32MiB` tampoco en reposo.
`GOGC=100` da 0,5 MB menos en reposo que `GOGC=50`, pero se mantiene 50 porque
su efecto útil es con sesión y carga, que aquí no se mide.

Qué queda y por qué no baja de ahí: el heap vivo (2,7 MB) son `init()` de
dependencias que sí se usan —descriptores protobuf de whatsmeow (~0,5 MB),
validator de gin (~0,3 MB), regex de `inflection` de gorm (~0,25 MB),
`x/net/html`, tipos de pgx—; el resto de la anónima es el runtime (metadatos
del GC, pilas, 7 hilos). Programas mínimos compilados aparte lo acotan: sólo
whatsmeow + `lib/pq` ocioso son 7,0 MB de RSS en macOS y 3,8 MB de RssAnon en
Linux; añadiendo gin + gorm/pgx, 10,3 MB y 5,2 MB. Bajar de ahí exige quitar
gin (a `net/http`) y gorm+pgx (a `database/sql` + `lib/pq`): ~−1,4 MB de
anónima y ~−3 MB de RSS en macOS, a cambio de reescribir 85 rutas, 71
llamadas a `ShouldBind*`/`BindJSON` y los repositorios gorm.

### Producción (EC2 3.130.244.177, 25-09-2026)

Lecturas de `docker stats` en el servidor, mismo día. Las dos instancias de
evolution-go estaban **desconectadas** (sin sesión) en las dos lecturas del Go,
así que comparan arranque contra arranque, no contra la operación con sesión.

| Contenedor | RAM | Nota |
|---|---:|---|
| `pjg_evolution_api` (Evolution API, Node) | 252,6 MiB | + `pjg_evolution_postgres` 53,6 MiB. Lo que se retira. |
| `pjg_evolution_go` antes de todo | 93,4 MiB | con 1 sesión (tabla siguiente) |
| `pjg_evolution_go` tras `7e6dd48` | 76,4 MiB | con 1 sesión (tabla siguiente) |
| `pjg_evolution_go` tras `7e6dd48`, recreado, 0 sesiones, a los 30 min | 46,8 MiB | cgroup 47,7 MiB |
| **`pjg_evolution_go` esta versión, 0 sesiones, a los 5 min** | **8,1 MiB** | cgroup 9,1 MiB, pico 12,7 MiB; imagen 301 → 248 MB |
| `pjg_evolution_go_postgres` | 66,0 MiB | no cambia con esta versión |
| `pjg_boticalima2_backend` (Laravel, PHP-FPM) | 138–150 MiB | el que atiende los webhooks |
| `pjg_emanuelpharma_backend` (Laravel) | 110–126 MiB | |
| `app_backend` (Laravel, apex) | 52–55 MiB | |
| `pjg_boticalima2_backend_go` (pod Go de boticas) | 8,0 MiB | |

Falta la lectura con sesión vinculada: es donde actúan `GOGC=50` y no
descargar el HistorySync. Se toma cuando se vincule un número en esta versión.

## Tres métricas de producción

Medición en producción del 25-09-2026, con una sesión WhatsApp conectada. No es
benchmark; son lecturas de `docker stats`, cgroup y `pg_stat_activity`.

| Métrica | Antes del ajuste | Después del ajuste |
|---|---:|---:|
| RAM actual del contenedor Go | 93,4 MiB | 76,4 MiB (**−18 %**) |
| Pico de memoria cgroup | 97,9 MiB | 80,3 MiB de 128 MiB |
| Conexiones PostgreSQL ociosas | 5 (`auth` 4 + `users` 1) | 3 (`auth` 2 + `users` 1) |

## Botones y listas interactivas (`INTERACTIVE_STYLE`)

`POST /send/button` con botones `reply` y `POST /send/list` admiten dos formas
de armar el proto, elegidas con la variable de entorno `INTERACTIVE_STYLE`
(`legacy` por defecto). Se lee en `pkg/config` y se aplica en
`pkg/sendMessage/service/interactive_builders.go`; las funciones de
construcción son puras y tienen pruebas sin red.

Motivo: medido el 25-09-2026, el formato `legacy` devuelve 200 y whatsmeow lo
entrega, pero el WhatsApp actual de Android **no pinta el mensaje** (ni texto
ni botones) cuando lo manda una cuenta no oficial. `viewonce` replica lo que
generan Baileys (`generateWAMessageContent` para `interactiveButtons`) y
Evolution API v2, que hoy sí se ve en Android/iOS.

| Estilo | `/send/button` (reply) | `/send/list` |
|---|---|---|
| `legacy` | `DocumentWithCaptionMessage → ButtonsMessage` + `MessageContextInfo{MessageSecret}` en la raíz. Nodo `<biz><interactive type="native_flow" v="1"><native_flow name="quick_reply"/></interactive></biz>`. | `DocumentWithCaptionMessage → ListMessage(SINGLE_SELECT)` + `MessageContextInfo{MessageSecret}` en la raíz. Nodo `<biz><list v="2" type="single_select"/></biz>`. |
| `viewonce` | `ViewOnceMessage → Message{MessageContextInfo{DeviceListMetadata{}, DeviceListMetadataVersion:2, MessageSecret}, InteractiveMessage{Header{title}?, Body{description}, Footer{footer}?, NativeFlowMessage{Buttons:[quick_reply × N], MessageParamsJSON:""}}}`. Cada botón lleva `ButtonParamsJSON = {"display_text":…,"id":…}`. Mismo nodo `<biz>` que legacy. | Mismo sobre `ViewOnceMessage → InteractiveMessage` con **un** botón `single_select` cuyo `ButtonParamsJSON` es `{"title":<buttonText>,"sections":[{"title":…,"rows":[{"header":"","title":…,"description":…,"id":<rowId>}]}]}`. Nodo `<biz><interactive type="native_flow" v="1"><native_flow name="single_select"/></interactive></biz>`. |

En los dos estilos, los chats 1:1 añaden `<bot biz_bot="1"/>` (los grupos no).
En `viewonce`, si viene `imageUrl` / `videoUrl`, el medio va en
`Header.Media` con `HasMediaAttachment=true` (el proto lo permite: `go doc
waE2E.InteractiveMessage_Header`). `Info.Type` del evento `SendMessage` pasa a
`InteractiveMessage` en `viewonce` (antes `ButtonsMessage` / `ListMessage`).

La respuesta del cliente llega en ambos casos como
`interactiveResponseMessage.nativeFlowResponseMessage.paramsJSON`:
`{"id":…,"display_text":…}` para botón y `{"id":…,"title":…,"description":…}`
para fila de lista. El evento `Message` la clasifica como `interactive response`
(no se descarta) y el evento `ButtonClick` rellena `buttonText` con
`display_text` o, si falta, con `title`.

Riesgos conocidos de `viewonce`: WhatsApp Web / Escritorio puede no pintar
interactivos dentro de `ViewOnceMessage`; iOS exige el `MessageSecret` (va
puesto); los atributos exactos del nodo `<biz>` que usa Baileys cambian entre
versiones y aquí se mantienen los del fork. Si un cliente deja de pintarlos,
volver a `legacy` es sólo cambiar la variable y reiniciar.

## Compilar y probar

Requiere Go 1.25 o posterior y PostgreSQL.

```bash
go test ./...
go vet ./...
go build ./cmd/evolution-go
```

Swagger está disponible en `/swagger/index.html` cuando el servicio se compila
sin la etiqueta `noswagger` (la imagen de producción la lleva puesta).
`go build` a secas compila todo (SQLite, MinIO, NATS, RabbitMQ, Swagger); la
imagen usa `GO_TAGS="noswagger nosqlite nomsgpack nominio nonats noamqp"`
(ver «Perfil de recursos»).

Para medir memoria hay una build de diagnóstico que **nunca** entra en la
imagen: `-tags pprofdiag` abre pprof y `/debug/memstats` en
`127.0.0.1:$PPROF_PORT` (6061 por defecto; `?gc=1` fuerza un GC, `?free=1`
llama a `debug.FreeOSMemory`). Con `GODEBUG=memprofilerate=1` el perfil del
heap cuenta cada asignación, incluidas las de los `init()`.
El despliegue de PjgFactSalud se coordina en el
[repositorio de la aplicación](https://github.com/davrv93/pjgfarma).

## Upstream y licencia

Este fork parte de
[evolution-foundation/evolution-go](https://github.com/evolution-foundation/evolution-go)
y usa [whatsmeow](https://github.com/tulir/whatsmeow) para el protocolo
WhatsApp. Consulta `LICENSE`, `NOTICE` y `TRADEMARKS.md` para términos y
atribuciones completas.
