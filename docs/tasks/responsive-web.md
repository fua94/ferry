---
status: completado
tipo: fix
prioridad: alta
tags: [web, responsive, accesibilidad]
---

# Corregir el layout responsive de la web

## Problema

La web embebida no se comporta igual en móvil, tablet y escritorio. Hay controles por debajo de
44 px y el `overflow-x: hidden` de `body` puede estar ocultando desbordamiento real en vez de
corregirlo.

## Criterio de aceptación

- A 390, 768 y 1280 px de viewport, `document.documentElement.scrollWidth` no supera
  `clientWidth`; no aparece scroll horizontal en `html` ni en `body`.
- Los controles táctiles (canales, adjuntar, enviar, copiar, descargar, acciones de mensaje)
  miden al menos 44×44 px en los tres anchos y no se solapan.
- Textos largos, nombres de archivo y tarjetas de mensaje no se salen de su contenedor.
- La barra de canales y el compositor siguen usables con el sidebar compacto.

## Archivos implicados

- `internal/webui/assets/app.css`
- `internal/webui/assets/index.html`
- `internal/webui/assets/app.js`
- `docs/channels-v2.md` (decisión 3)
