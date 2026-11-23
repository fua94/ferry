# Conventional Commits v1.0.0 — reglas y heurísticas de agrupación

Referencia de la skill `commit-planner`. El spec completo está en
https://www.conventionalcommits.org/en/v1.0.0/

## Formato del mensaje

```
type(scope): description

[body opcional]

[footer opcional]
```

En este repo se usa **solo el header**: una línea, sin body ni footer.

### Tipos permitidos

| type | Uso |
|------|-----|
| `feat` | funcionalidad nueva |
| `fix` | corrección de bug |
| `docs` | solo documentación |
| `style` | formato/whitespace sin cambio de comportamiento |
| `refactor` | restructuring sin añadir ni quitar funcionalidad |
| `perf` | mejora de rendimiento |
| `test` | añadir o corregir tests |
| `build` | sistema de build o dependencias |
| `ci` | CI, pipelines, configuración de integración |
| `chore` | mantenimiento que no modifica src ni tests |
| `revert` | reversión de un commit previo |

### Breaking changes

Añade `!` después del tipo (o del scope) y un footer `BREAKING CHANGE: <descripción>`:

```
feat(api)!: change channel response shape
```

### Scope

Opcional. Es el módulo, componente o área afectada. Usa nombres de la estructura real del repo:

`server`, `store`, `web`, `channels`, `files`, `devices`, `auth`, `ios`, `android`, `api`,
`docs`, `board`, `scripts`, `agents`.

### Description

- En **inglés** (el resto de la documentación del repo está en español, los commits no).
- **Imperativo** y en minúsculas: `add`, `fix`, `remove`, `extract`.
- Sin punto final.
- La línea completa **no excede 100 caracteres**.

### Ejemplos

```
feat(channels): drop membership checks
fix(web): avoid horizontal overflow at 390px
docs(channels): specify channel v2 behaviour
refactor(store): extract channel deletion cascade
test(store): cover channel deletion cascade
chore(board): mirror task files to vikunja
```

## Heurísticas de agrupación (archivos relacionados)

Regla general: **cada commit contiene un conjunto de archivos relacionados entre sí**.

Orden de decisión:

1. **Mismo cambio lógico.** ¿El diff de estos archivos cuenta la misma historia? Si sí, van juntos.
2. **Mismo `type` + `scope`.** Dos archivos con el mismo mensaje encajan en un commit.
3. **Misma capa/módulo.** Respeta la estructura del repo:
   - Server: `cmd/`, `internal/ferry/`
   - Web embebida: `internal/webui/`
   - Contrato: `api/`
   - Apps: `ios/`, `android/`
   - Docs y flujo: `docs/`, `.opencode/`, `scripts/`, `AGENTS*.md`
4. **Código + su test** en el mismo commit cuando el test existe para ese código.
5. **Formato separado** de la lógica: un reformateo masivo no debe contaminar un commit funcional.

### Límites

- Un archivo → **un solo** commit. No se divide por hunks (`git add -p` no es automatizable de forma
  fiable y el plan debe ser reproducible).
- Si un archivo mezcla dos concerns, va al grupo dominante y se marca con `⚠️` en el plan para que el
  usuario decida.
- Renames y deletions: una sola entrada por archivo.
- Nada de granularidad artificial: si todo el cambio es un concern, es **un** commit.
