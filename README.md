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

Swagger está disponible en `/swagger/index.html` cuando el servicio está activo.
El despliegue de PjgFactSalud se coordina en el
[repositorio de la aplicación](https://github.com/davrv93/pjgfarma).

## Upstream y licencia

Este fork parte de
[evolution-foundation/evolution-go](https://github.com/evolution-foundation/evolution-go)
y usa [whatsmeow](https://github.com/tulir/whatsmeow) para el protocolo
WhatsApp. Consulta `LICENSE`, `NOTICE` y `TRADEMARKS.md` para términos y
atribuciones completas.
