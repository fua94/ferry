---
status: completado
tipo: todo
prioridad: media
tags: [canales, api, borrado]
---

# Borrar un canal en cascada

## Problema

Hoy no existe `DELETE` de canales. Un canal creado por error queda para siempre, junto con sus
mensajes, retractions, membresías y archivos.

## Criterio de aceptación

- `DELETE /api/v1/channels/{channel}` borra el canal y, en la misma operación, sus mensajes,
  retractions, filas de `device_channels` y blobs de archivos.
- `default` responde `409` con un código estable y no se modifica.
- Un canal inexistente responde `404`.
- Cualquier dispositivo autenticado puede borrar; no se añaden roles ni propietarios.
- Los enlaces de descarga de archivos del canal borrado dejan de resolver.
- Tests: canal con texto y archivo, retraction, `default`, `404` y segundo borrado.

## Archivos implicados

- `internal/ferry/server.go`
- `internal/ferry/store.go`
- `api/openapi.yaml`
- `internal/webui/assets/app.js` (si se expone el control en la web)
- `docs/channels-v2.md` (decisión 2)
