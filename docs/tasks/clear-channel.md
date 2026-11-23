---
status: completado
tipo: todo
prioridad: media
tags: [canales, web, api, borrado]
---

# Limpiar el chat de un canal

## Problema

Hoy la única forma de vaciar un canal es borrarlo entero con `DELETE /api/v1/channels/{id}`, lo
que pierde el nombre, su lugar en la lista y sus filas de `device_channels`. Para reutilizar un
canal hace falta una acción menos destructiva que vacíe solo su línea de tiempo.

## Criterio de aceptación

- `DELETE /api/v1/channels/{channel_id}/messages` responde `204 No Content` con
  `Cache-Control: no-store`.
- La limpieza ocurre en una sola transacción: valida el canal, lee sus mensajes, inserta una
  retraction por cada mensaje con una secuencia posterior al máximo global y borra los mensajes.
- `ListRetractions` publica esas retractions al resto de dispositivos en su siguiente consulta.
- Los blobs de los mensajes de tipo archivo se eliminan después del commit, ignorando
  `os.ErrNotExist`.
- Se conservan la fila del canal y las filas de `device_channels`; no se borran retractions ya
  existentes.
- `default` se puede limpiar. Un canal vacío responde `204` idempotente, uno inexistente
  `404 not_found` y un slug inválido `400 invalid_request`.
- La web muestra en el header del canal, a la derecha del título, un botón de limpieza visible para
  el canal activo, también `default`, siempre habilitado, con confirmación y recarga del timeline
  cuando el canal limpiado es el activo. El sidebar conserva solo el borrado en cada fila.
- Tests HTTP cubren texto y archivo, retractions, conservación de `default`, segundo clear, clear de
  `default`, `404` y `400`; los contratos de la web cubren la acción, el endpoint y los estilos.

## Archivos implicados

- `internal/ferry/store.go`
- `internal/ferry/server.go`
- `api/openapi.yaml`
- `internal/webui/assets/app.js`
- `internal/webui/assets/app.css`
- `internal/ferry/channel_test.go`
- `internal/webui/channel_test.go`
- `docs/channels.md`

## Pendiente

Verificado por el usuario y aprobado el 2026-11-23. Queda pendiente el despliegue en el puerto
42817 (imagen `0.2.1`), que requiere OK explícito.
