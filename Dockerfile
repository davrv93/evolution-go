FROM golang:1.25.0-alpine AS build

RUN apk update && apk add --no-cache git build-base libjpeg-turbo-dev libwebp-dev

WORKDIR /build

# Copiar apenas arquivos de dependências primeiro para cachear o download
COPY go.mod go.sum ./

# whatsmeow agora vem do proxy oficial (go.mau.fi/whatsmeow, sem replace local) —
# não há mais submódulo whatsmeow-lib para copiar.
RUN go mod download

# Copiar o restante do código
COPY . .

ARG VERSION=dev
# `noswagger` deja fuera del binario la UI y el spec de Swagger (~10 MB de
# binario; Laravel no los usa). Para volver a tenerlos: --build-arg GO_TAGS="".
ARG GO_TAGS=noswagger
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
