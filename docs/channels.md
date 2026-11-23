# Canales: líneas de tiempo separadas y visibilidad compartida

Este documento describe el contrato de canales de Ferry y se mantiene versionado junto al código.

Ferry es una línea de tiempo compartida; eso explica buena parte de su tamaño. Una instalación con
un teléfono, un portátil y una tableta puede necesitar separar el trabajo de lo personal sin
ejecutar un servidor por cada línea de tiempo.

Los canales son **opt-in y aditivos**.

## Comportamiento

- Una petición que no menciona un canal se comporta exactamente como antes: lee y escribe
  `default`. Las apps nativas de iOS y Android siguen funcionando sin cambios y solo ven `default`.
- Las tablas `channels` y `device_channels` existen; `messages.channel_id` y
  `retractions.channel_id` se añaden mediante el camino de migración `ALTER TABLE` con
  `DEFAULT 'default'`, de modo que una base v1.0.5 abre sin cambios y cada dispositivo existente
  conserva una fila histórica en `default`.
- **Un canal es visibilidad, no permiso.** Cualquier dispositivo autenticado ve, lee y escribe en
  todos los canales. No hay membresía, propietario, rol ni invitación que limite una lectura o una
  escritura.
- `device_channels` permanece en el esquema y en los datos por compatibilidad de migración, pero
  ninguna lectura ni escritura la consulta. No se borran filas ni columnas al migrar; las filas
  heredadas solo se eliminan cuando se borra su canal.
- `GET /api/v1/channels` devuelve todos los canales del servidor. El campo `joined` se conserva
  siempre en `true` para no romper a clientes antiguos; ya no significa membresía, la web no lo lee
  y puede retirarse en un cambio posterior.
- `POST /api/v1/channels` crea un canal. El cuerpo usa el campo `name`, no `slug`: el valor tiene
  forma de slug (1–32 caracteres, minúsculas, dígitos y guiones internos), pero el campo se llama
  `name`. Un nombre existente responde `409 channel_exists`; uno inválido responde `400`.
- `POST /api/v1/channels/join` es un no-op idempotente conservado por compatibilidad de API:
  responde `204` si el canal existe y `404` si no existe, y no escribe en `device_channels`. Ya no
  es necesario para ver ni para escribir en un canal.
- La regla de `404` anti-fingerprint para «canales ajenos» desaparece: ya no existen canales
  ajenos. Un canal que no existe sigue siendo `404 not_found`.
- `DELETE /api/v1/channels/{channel_id}` borra el canal y, dentro de una transacción, sus mensajes,
  sus retractions, sus filas de `device_channels` y el propio canal. Los blobs de los archivos se
  eliminan del disco después del commit; los enlaces de descarga emitidos para ellos dejan de
  resolver. Cualquier dispositivo autenticado puede borrar: el modelo no tiene dueño ni roles.
  - `default` está protegido y responde `409 channel_protected`.
  - Un canal inexistente responde `404 not_found`.
  - Un segundo `DELETE` del mismo id responde `404 not_found`, igual que el resto de borrados de
    la API; no hay idempotencia con `204`.
  - Un id con formato inválido responde `400 invalid_request`.
- Cualquier dispositivo autenticado puede retractar cualquier mensaje de cualquier canal. Se
  mantiene el modelo sin dueño en lugar de introducir roles.
- La contraseña de acceso sigue siendo del servidor completo. Las contraseñas por canal serían un
  sistema de permisos, que Ferry deliberadamente no tiene.

## Web app

- La lista de canales del sidebar muestra todos los canales; ninguno queda atenuado por falta de
  membresía. El clic en un canal solo cambia de línea de tiempo: la web no llama a
  `POST /api/v1/channels/join` ni lee el campo `joined`.
- Cada fila ofrece una acción de borrado con confirmación. `default` la muestra deshabilitada
  porque el servidor responde `409 channel_protected`; un `404` significa que el canal ya no
  existía y la lista se refresca. Si se borra el canal activo, la vista vuelve a `default`.
- La dirección de conexión y el código QR siguen llevando el canal activo, de modo que un
  dispositivo que los escanee abre la línea de tiempo elegida en lugar de `default`.
- Por debajo de 760 px el sidebar se convierte en un drawer off-canvas con un botón de apertura
  visible, en lugar de comerse el contenido o reducirse a una barra de iconos. Los controles
  táctiles usan un mínimo compartido de 44 px y no hay scroll horizontal a 390, 768 ni 1280 px.
  La aceptación de esa medida se comprueba en un navegador durante la fase de verificación visual.

## Tests y verificación

- `go build ./...`, `go vet ./...` y `go test -race -count=1 ./...` pasan.
- Las pruebas cubren la migración desde un esquema v1.0.5, la lectura y escritura de todos los
  canales por un dispositivo sin membresía, el `join` repetido sin filas nuevas, el borrado en
  cascada de mensajes, retractions, membresías y blobs, `default` protegido, el `404` de un canal
  inexistente y el segundo borrado.

## Scope note

`docs/product-core.md` incluye la «gestión de grupos» fuera de alcance. Los canales son algo más
pequeño: sin cuentas, sin invitaciones, sin roles, sin presencia y sin administración. Un canal es
*visibilidad*, no permiso. Una instalación que prefiera una sola línea de tiempo puede ignorar la
función por completo, porque el camino por defecto conserva el comportamiento anterior.
