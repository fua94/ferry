---
status: completado
tipo: todo
prioridad: alta
tags: [canales, visibilidad, api]
---

# Quitar la membresía como regla de visibilidad

## Problema

Los canales funcionan hoy como membresía: un dispositivo solo lee y escribe los canales a los que
se unió, `GET /channels` devuelve `joined`, y un canal existente sin membresía responde `404` para
no revelar que existe. Eso convierte un canal en un permiso encubierto.

## Criterio de aceptación

- Todos los dispositivos autenticados ven, leen y escriben en todos los canales.
- `GET /api/v1/channels` ya no devuelve `joined`; el cliente no necesita unirse antes de abrir.
- El `404` anti-fingerprint desaparece para canales existentes; solo un canal inexistente da `404`.
- `POST /api/v1/channels/join` responde `204` si el canal existe y `404` si no, como no-op
  idempotente, sin escribir en `device_channels`.
- `device_channels` puede quedarse en el esquema y en los datos, pero ningún camino de decisión la
  consulta. La migración no borra filas ni columnas.
- `default` sigue siendo el canal implícito cuando la petición no menciona `channel`.
- Tests: lectura y escritura entre canales, `join` repetido, canal inexistente y compatibilidad sin
  parámetro `channel`.

## Archivos implicados

- `internal/ferry/store.go`
- `internal/ferry/server.go`
- `api/openapi.yaml`
- `internal/webui/assets/app.js`
- `docs/channels-v2.md` (decisión 1)
