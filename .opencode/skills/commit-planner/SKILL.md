---
name: commit-planner
description: Analiza los cambios sin commitear frente a la punta de la rama actual (HEAD), los agrupa por archivos relacionados y propone una lista de commits Conventional Commits para aprobación del usuario antes de escribir en la historia. Use when the user asks to commit changes, split changes into several commits, or review a commit plan before committing.
license: MIT
compatibility: opencode
metadata:
  author: ferry
  version: "1.0"
---

Genera un **plan de commits** a partir de los cambios actuales del repositorio, agrupados por
archivos relacionados, y lo somete a aprobación del usuario **antes** de escribir en la historia.

Regla inviolable: **ningún commit se escribe sin aprobación explícita del usuario.**

El usuario puede invocar la skill con `/commit` (comando wrapper) o por disparador semántico
("commitea esto", "haz varios commits con estos cambios", "revisa los cambios pendientes").

---

## Input

Sin parámetros obligatorios: todo se deduce del estado del repo.

Si el usuario da un alcance ("solo los cambios de `docs/`", "solo el fix de canales"), aplícalo como
filtro en la Fase 1 y menciónalo en el plan.

---

## Fase 0 — Recolectar cambios

Ejecuta con la herramienta `bash`:

```bash
git status --porcelain=v1 -uall
git diff HEAD --name-status
git diff HEAD
```

- El alcance es **todo lo que difiere de la punta de la rama actual (`HEAD`)**: staged + unstaged
  + untracked. **No** es solo el index (`git diff --staged`).
- Los untracked **no** aparecen en `git diff HEAD`: se enumeran desde `git status` y se incluyen
  como archivos de su grupo (su diff es el archivo completo).
- Si no hay ninguna diferencia (status limpio y `git diff HEAD` vacío): informa
  **"No hay cambios para commitear"** y detente. No sigas a la Fase 1.
- Nunca ejecutes `git fetch`, `git pull`, `git push` ni nada que reescriba la historia.

Guarda la lista de archivos con su estado (`A`/`M`/`D`/`R`) y el diff completo para analizarlo.

---

## Fase 1 — Agrupar por archivos relacionados

Objetivo: **cada commit contiene un conjunto de archivos relacionados entre sí**.

Criterios, en este orden de peso:

1. Mismo cambio lógico: el diff de esos archivos cuenta la misma historia.
2. Mismo `type` y mismo `scope` (ver `references/conventional-commits.md`).
3. Misma capa/módulo: `cmd/`, `internal/ferry/`, `internal/webui/`, `api/`, `ios/`, `android/`,
   `docs/`, `scripts/`, `.opencode/`.
4. Código y su test en el **mismo** grupo cuando el test existe para ese código.
5. Cambios puramente de formato (whitespace, orden de imports) separados del cambio funcional.

Reglas duras:

- Cada archivo aparece en **exactamente un grupo**. Ningún archivo en dos grupos, ningún grupo vacío.
- Renames y deletes cuentan como **un solo archivo** (usa la ruta nueva y menciona la vieja si aporta).
- **No** se hace split por hunks dentro de un archivo. Si un archivo mezcla dos concerns distintos,
  asígnalo al grupo dominante y **marca ese archivo con `⚠️`** en el plan para que el usuario lo revise.
- Ordena los grupos por **dependencia lógica**: tipos/config/infra antes que el código que los consume.
- Si todo el cambio es un único concern → **un solo grupo**. No inventes granularidad artificial.

---

## Fase 2 — Mensajes

Un mensaje por grupo, con las reglas de `references/conventional-commits.md`:

```
type(scope): description
```

- `type`: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
  Si el diff contiene breaking changes, añade `!` tras el tipo (o tras el scope).
- `scope` (opcional): módulo, componente o área afectada (p. ej. `channels`, `web`, `server`, `docs`).
- `description`: resumen breve, **imperativo**, en **inglés**, en minúsculas, **sin punto final**.
- La línea **no debe exceder 100 caracteres**. Solo el header: sin body, sin footer, sin `git commit -m`.

---

## Fase 3 — Mostrar el plan

Presenta el plan en la conversación como tabla markdown:

| # | Mensaje | Archivos relacionados |
|---|---------|-----------------------|
| 1 | `feat(channels): drop membership checks` | `internal/ferry/store.go`, `internal/ferry/server.go` |
| 2 | `test(channels): cover cross-channel access` | `internal/ferry/server_test.go` |
| 3 | `docs(channels): specify channel v2 behaviour` | `docs/channels-v2.md` |

Añade `⚠️` junto a los archivos que mezclan concerns. Indica el total de commits y de archivos.

**El plan completo va en el mensaje de la conversación, NO dentro del texto de la pregunta.**
La `question` debe ser una sola línea corta: el usuario ya tiene el detalle en pantalla.

Restricciones de la herramienta `question` que debes respetar:

- `header`: máximo **30 caracteres**.
- `label` de cada opción: **1–5 palabras**. El mensaje completo del commit va en el `description`
  de la opción, nunca en el `label`.
- La opción **"Type your own answer"** se añade automáticamente: no añadas una opción "Otro" propia.
- El texto de la `question` debe ser **conciso**: una frase, sin tablas ni listas de archivos.

---

## Fase 4 — Aprobación con `question`

Bucle de dos llamadas. **Repite desde la Llamada A tras cada ajuste.**

### Llamada A — decisión global

- **Header:** `Plan de commits`
- **Pregunta:** `¿Cómo deseas continuar?`
- **Opciones** (`multiple: false`):
  - `Aprobar y commitear` → pasa a la Fase 5.
  - `Ajustar mensajes` → Llamada B.
  - `Reagrupar archivos` → pide guía al usuario (respuesta libre) y vuelve a la Fase 1.
  - `Cancelar` → termina sin modificar nada.

### Llamada B — elegir commits a ajustar (solo si eligió "Ajustar mensajes")

Antes de la llamada, escribe en la conversación: *"Marca los commits a cambiar y escribe el texto
nuevo en 'Type your own answer' (formato `#1: <mensaje>`)."*

- **Header:** `Ajustar mensajes`
- **Pregunta:** `¿Qué mensajes quieres cambiar?`
- **Opciones** (`multiple: true`), una por commit:
  - `label`: `#1 feat(channels)` (corto)
  - `description`: mensaje completo + archivos del commit.
- Tras la selección, por cada commit marcado haz una `question` individual:
  - **Header:** `Mensaje #N`
  - **Pregunta:** `¿Lo mantienes o lo reescribes?`
  - **Opciones:** `Mantener` | `Reescribir` (el texto libre aporta el mensaje nuevo).
- Regenera el plan, muéstralo de nuevo en la conversación y **vuelve a la Llamada A**.

Nunca ejecutes la Fase 5 sin una "Aprobar y commitear" explícita en la Llamada A.

---

## Fase 5 — Ejecutar los commits

```bash
git reset --mixed HEAD          # normaliza el índice; el working tree queda intacto
# por cada grupo, en orden:
git add -- <paths-del-grupo>
git commit -m "<mensaje>"
```

- Usa `git add -- <paths>` con las rutas **exactas** del grupo. Nunca `git add .` ni `git add -A`.
- Tras el último commit, verifica y muestra el resultado:

```bash
git status --porcelain
git log --oneline -<N>
```

- Si un grupo tiene archivos untracked, `git add -- <paths>` los incluye (no tienen diff en HEAD).
- Si el usuario excluyó archivos del plan, deben quedar **sin commitear** y visibles en `git status`.

### Guardrails

- **Nunca** `git commit -a`, `git add .`, `git add -A`, `git commit --amend`, `git push`,
  `git reset --hard`, `git checkout --`, `git clean`.
- No reescribas commits ya existentes: solo se crean commits nuevos a partir de `HEAD`.
- Si un comando falla, **detente**, informa del error y no sigas con los grupos restantes.
- Si el diff contiene secretos (`.env`, claves, tokens, credenciales), avisa al usuario antes de
  aprobarlos y marca esos archivos con `⚠️`.
- No stages archivos generados ni ruido (`build/`, `DerivedData/`, `node_modules/`, `dist/`,
  `.env.local`, `artifacts/`).
- Idioma: la comunicación con el usuario es en **español**; los mensajes de commit en **inglés**.
