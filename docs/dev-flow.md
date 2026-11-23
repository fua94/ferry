# Flujo de desarrollo

Este documento fija cómo se trabaja en Ferry y cómo se relacionan las fichas de `docs/tasks/` con
el tablero de Vikunja. `AGENTS.md` sigue siendo el contrato del repo; si algo aquí choca con él,
manda `AGENTS.md`.

## Reglas

1. **Rama actual.** Se trabaja en el worktree y la rama actuales. No se crean ramas, worktrees ni
   PRs por iniciativa propia.
2. **Ningún commit sin aprobación explícita del usuario.** El único camino es la skill
   `commit-planner` (`/commit`): primero muestra el plan, después espera la aprobación y solo
   entonces escribe la historia. Tampoco se hace push, se abre PR, se despliega ni se publica sin
   permiso explícito.
3. **El .md es la fuente de verdad.** `docs/tasks/*.md` define título, estado, tipo, prioridad y
   tags. El tablero es un espejo: se puede reconstruir desde los archivos y nunca se edita a mano
   como si fuera la fuente.
4. **Sincronizar tras tocar `docs/tasks/`.** Ejecutar `npm run board:sync` después de crear,
   renombrar o cambiar una ficha. Para ver el plan sin escribir: `npm run board:sync:dry`.
5. **Nada de despliegues sin permiso.** Build, TestFlight, App Store, Docker o cualquier acción
   externa requieren autorización explícita; un build correcto no es permiso.
6. **Done solo con el OK del usuario.** Una ficha pasa a `status: completado` (bucket Done) solo
   cuando el usuario confirma que verificó el cambio y dio el OK. Mientras tanto se queda en
   `en progreso`, aunque el código y los tests estén listos.

## Fichas de tareas

Un solo directorio y un solo board, tanto para tareas como para fixes:

```markdown
---
status: por-hacer
tipo: fix
prioridad: alta
tags: [web, responsive]
---

# Título de la ficha

Problema, criterio de aceptación y archivos implicados.
```

- `status`: `por-hacer` (o `por hacer`), `en-progreso` (o `en progreso`) o `completado`.
- `tipo`: `fix` o `todo`; se escribe en la descripción de la tarjeta para que un solo board
  distinga ambas cosas.
- `prioridad`: `alta` = 3, `media` = 2, `baja` = 1. Si falta, no se toca la prioridad de la tarjeta.
- `tags`: lista simple; se refleja en la descripción de la tarjeta.

`docs/tasks/archive/` se ignora: una ficha archivada deja de sincronizarse.

## Tablero

Constantes fijas en `scripts/lib/board.mjs` (no se redescubren en cada ejecución):

| Dato | Valor |
|---|---|
| Proyecto | `6` — «ferry / tareas y fixes» |
| Vista kanban | `35` |
| `por hacer` | bucket `40` (To-Do) |
| `en progreso` | bucket `41` (Doing) |
| `completado` | bucket `42` (Done) |

- El emparejamiento es determinista: ancla `Ruta: docs/tasks/x.md` en la descripción; si la tarjeta
  se creó antes de tener ancla, se empareja por título normalizado y se escribe el ancla.
- El espejo **crea** tarjetas faltantes, **mueve** de columna y **actualiza** título, prioridad,
  tipo, tags y descripción. **Nunca borra tarjetas**; las que no tienen .md se reportan como
  huérfanas al final.
- La segunda ejecución debe reportar **0 cambios**. Un diff vacío no significa «todo al día» si
  Vikunja no responde: en ese caso el script falla con `BOARD_UNREACHABLE`.
- Token: `VIKUNJA_TOKEN` o `~/.vikunja-token`. URL: `VIKUNJA_URL` (default
  `http://127.0.0.1:3456`).

## Hook de commit

`.githooks/post-commit` sincroniza el board solo cuando el commit tocó `docs/tasks/`. Se instala una
vez por clon:

```bash
git config core.hooksPath .githooks
```

`VIKUNJA_SKIP=1` lo desactiva. Si `node` no está o el board falla, el hook avisa y **sale 0**: un
board caído nunca bloquea un commit.

## Verificación antes de entregar

- `git diff --check`.
- `npm run board:sync:dry` para revisar el plan.
- `npm run board:sync` dos veces; la segunda debe dar `0 creadas, 0 movidas, 0 actualizadas`.
- Pruebas proporcionales al riesgo del cambio; no se declara «verificado» sin evidencia reproducible.

## Puertos: produccion vs pruebas

- **42817 es el puerto de produccion.** Nunca recrear ese contenedor para probar una imagen nueva.
- Todo build de prueba va en un **puerto alternativo (42818)**, con su propio volumen y su propio nombre de contenedor (`ferry-test`), hasta que el usuario de el **OK explicito** de corte.
- Solo con ese OK se cambia el `image:` de `~/ferry/compose.yaml` y se recrea el contenedor de 42817.
- Regla registrada el 2026-11-23 tras un recreate directo en 42817 sin autorizacion (correccion del usuario).
