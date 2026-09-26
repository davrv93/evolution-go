FROM golang:1.25.0-alpine AS build

RUN apk update && apk add --no-cache git build-base libjpeg-turbo-dev libwebp-dev

WORKDIR /build

# Copiar apenas arquivos de dependências primeiro para cachear o download
COPY go.mod go.sum ./
# whatsmeow va con un parche local (third_party/whatsmeow/PATCH-PJG.md): el
# `replace` de go.mod apunta ahí, así que tiene que existir antes del download.
COPY third_party ./third_party
RUN go mod download

# Copiar o restante do código
COPY . .

ARG VERSION=dev
# Etiquetas que dejan fuera del binario lo que esta instalación no usa (ver
# README, «Perfil de recursos»). Cada una quita código Y el heap que su init()
# reserva al arrancar, aunque la función esté apagada:
#   noswagger  UI y spec de Swagger (~10 MB de binario; Laravel no los usa).
#   nosqlite   driver SQLite: la sesión va a POSTGRES_AUTH_DB (obligatorio).
#   nomsgpack  códec msgpack de gin (etiqueta oficial de gin); nadie lo pide.
#   nominio    cliente MinIO/S3 (MINIO_ENABLED=false); arrastraba goccy/go-json,
#              que reservaba ~2 MB de heap en Linux al arrancar.
#   nonats     cliente NATS (sin NATS_URL).
#   noamqp     cliente RabbitMQ (sin AMQP_URL).
# Si se enciende una de esas funciones con el binario sin ella, el arranque
# falla con un mensaje claro. Para compilarlo todo: --build-arg GO_TAGS="".
ARG GO_TAGS="noswagger nosqlite nomsgpack nominio nonats noamqp"
# -trimpath y -s -w quitan rutas de compilación, tabla de símbolos y DWARF:
# menos imagen y menos páginas que mapear; no cambian el comportamiento.
RUN CGO_ENABLED=1 go build -trimpath -tags "${GO_TAGS}" \
    -ldflags "-s -w -X main.version=${VERSION}" -o server ./cmd/evolution-go

FROM alpine:3.19.1 AS final

# poppler-utils provides pdftoppm, used to rasterize PDF page 1 for /send/media document thumbnails
RUN apk update && apk add --no-cache tzdata ffmpeg libjpeg-turbo libwebp poppler-utils

WORKDIR /app

COPY --from=build /build/server .
COPY --from=build /build/manager/dist ./manager/dist
COPY --from=build /build/VERSION ./VERSION

ENV TZ=America/Sao_Paulo

ENTRYPOINT ["/app/server"]
