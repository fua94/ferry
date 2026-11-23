# Canales v2: visibilidad, borrado y responsive

> Especificación corta de las tres mejoras acordadas para el trabajo posterior. No implementa
> código: fija el contrato y la aceptación. El estado actual se describe solo como evidencia; la
> fuente de verdad del comportamiento presente es el código.

## Alcance

Un canal es **visibilidad, no permiso**. Ferry sigue sin cuentas, sin roles, sin invitaciones y sin
administración. Cualquier dispositivo con un token válido del servidor puede ver, leer y escribir
en todos los canales. La contraseña de acceso sigue siendo del servidor completo.

## Estado observado (2026-11-23)

- **Observed**: la visibilidad se decide por membresía en `device_channels`
  (`internal/ferry/store.go`). `GET /api/v1/channels` devuelve `joined` por dispositivo y un canal
  existente del que el dispositivo no es miembro responde `404`, igual que uno inexistente.
- **Observed**: `POST /api/v1/channels` solo acepta `name`. Enviar `{"slug":"..."}` responde
  `400 invalid_request` con el texto `request body must contain only name`. Un canal existente
  responde `409 channel_exists`.
- **Observed**: no existe `DELETE` de canales; solo `GET /api/v1/channels`,
  `POST /api/v1/channels` y `POST /api/v1/channels/join`.
- **Observed**: `POST /api/v1/channels/join` es el único camino para que un dispositivo que ya
  tiene token vea otro canal.

## Decisión 1 — Sin membresía

**Decided (usuario, 2026-11-23)**: eliminar la membresía como regla de acceso. Todos los
dispositivos ven, leen y escriben en todos los canales.

Contrato resultante:

- `GET /api/v1/channels` devuelve todos los canales sin el campo `joined`.
- Desaparece el `404` anti-fingerprint para un canal existente del que el dispositivo no es
  miembro. Un canal inexistente sigue respondiendo `404`.
- `POST /api/v1/channels/join` queda como no-op idempotente por compatibilidad: `204` si el canal
  existe y `404` si no existe, sin escribir membresías.
- `device_channels` puede permanecer en el esquema y en los datos, pero queda fuera del camino de
  decisión. La migración no es destructiva: no se borran filas ni columnas.
- `default` sigue existiendo y sigue siendo el canal implícito cuando la petición no menciona
  `channel`.

**Recommended**: retirar `joined` de la respuesta y de la web en el mismo cambio, en lugar de
mantener un campo que siempre vale lo mismo y que invita a volver a usarlo. Mantener
`JoinChannel` en el store solo si otro camino lo necesita; si no, el handler responde sin tocar la
base.

## Decisión 2 — Borrado de canal en cascada

**Decided (usuario, 2026-11-23)**: un canal se puede borrar y el borrado elimina en cascada sus
mensajes, retractions, archivos y membresías. Cualquier dispositivo autenticado puede borrar
cualquier canal; no hay propietario ni rol.

Contrato resultante:

- `DELETE /api/v1/channels/{channel}` (ruta recomendada; el patrón exacto se fija al implementar).
- `default` está protegido: `409` con un código estable (por ejemplo `default_channel`) y el canal
  no cambia.
- Canal inexistente: `404`.
- La cascada incluye las filas de `messages` del canal, sus filas de `retractions`, sus filas de
  `device_channels` y los blobs de archivos en disco. Los enlaces de descarga emitidos para esos
  archivos dejan de resolver.
- El modelo sin dueño es deliberado: es el mismo criterio que ya permite que cualquier miembro
  retracte cualquier mensaje. No se introducen roles para el borrado.

**Recommended**: implementar la cascada en el store dentro de una transacción, recoger primero los
`blob_name` y borrar los archivos de disco después del commit. Si falla el borrado de un blob, la
base ya quedó consistente; el archivo huérfano se registra y se limpia en una pasada posterior.
Añadir tests para: canal con texto y archivo, retraction asociada, `default` protegido, `404`, y un
segundo borrado del mismo canal.

## Decisión 3 — Responsive de la web

**Decided (usuario, 2026-11-23)**: corregir el layout responsive con aceptación medible.

Aceptación:

- A 390, 768 y 1280 px de ancho de viewport no hay scroll horizontal:
  `document.documentElement.scrollWidth <= document.documentElement.clientWidth`, y lo mismo en
  `body`.
- Todos los controles táctiles (botones, enlaces de acción, selector de canal, adjuntar, enviar,
  copiar y descargar) tienen un área interactiva de al menos 44×44 px en esos tres anchos, sin
  solaparse entre sí.
- El contenido no se corta ni se sale de la tarjeta del mensaje; los textos largos y los nombres de
  archivo siguen partiendo línea o truncándose con elipsis.
- La barra de canales y el compositor siguen siendo usables con el sidebar compacto de 56 px.

**Observed**: `body` ya declara `overflow-x: hidden`, que puede ocultar el desbordamiento en vez de
corregirlo; la aceptación debe medir `scrollWidth`, no confiar en que no aparezca la barra. Varios
controles miden entre 32 y 42 px.

**Recommended**: usar un mínimo compartido (por ejemplo `--touch-target: 44px`) en lugar de repetir
medidas por selector, y probar con el viewport del navegador, no solo con capturas estáticas.

## Corrección documental: `name`, no `slug`

**Observed**: `docs/channels.md` describe el create como «slug» y habla de «existing slug». La
implementación y `api/openapi.yaml` usan `name`; el esquema `ChannelID` describe el formato de slug,
pero el campo del cuerpo es `name`.

**Decided (usuario, 2026-11-23)**: la documentación y los ejemplos deben hablar de `name`. El valor
sigue teniendo forma de slug (1–32 caracteres, minúsculas, dígitos y guiones internos), pero
renombrar el campo no forma parte de estas tres mejoras.

## No objetivos

- Cuentas, roles, propietarios, invitaciones, permisos por canal o contraseñas por canal.
- Canales privados o membresía selectiva.
- Cambiar el modelo de tokens o la contraseña de acceso.
- TLS o exposición a Internet.

## Criterios de aceptación globales

1. Un dispositivo nuevo ve todos los canales y puede escribir en cualquiera sin llamar a `join`.
2. `join` sobre un canal existente responde `204` y no crea filas; sobre uno inexistente, `404`.
3. Borrar un canal no deja mensajes, retractions, membresías ni blobs huérfanos; `default`
   responde `409`.
4. La web pasa las tres anchuras sin scroll horizontal y con objetivos táctiles de al menos 44 px.
5. `docs/channels.md` y `api/openapi.yaml` coinciden en `name` y en la ausencia de `joined` cuando
   se implemente.
