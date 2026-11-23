#!/usr/bin/env node
/**
 * scripts/board-sync.mjs — espeja `docs/tasks/*.md` al tablero kanban de Vikunja.
 *
 * Regla del repo: **el .md es la fuente de verdad -> el tablero es un espejo**.
 * El script solo empuja estado desde los archivos; nunca escribe en los .md ni
 * borra tarjetas. Es idempotente: la segunda pasada reporta 0 cambios.
 *
 * Fuente de estado:
 *   docs/tasks/*.md          (frontmatter: status, tipo, prioridad, tags)
 *   docs/tasks/archive/      se ignora (tareas cerradas)
 *
 * Emparejamiento tarjeta <-> archivo:
 *   1. ancla de ruta en la descripción ("Ruta: docs/tasks/x.md")
 *   2. título normalizado idéntico (y se reescribe el ancla para la próxima vez)
 *
 * Uso:
 *   node scripts/board-sync.mjs --dry-run   # muestra el plan y no toca nada
 *   npm run board:sync                      # aplica
 *   npm run board:sync:dry                  # alias del dry-run
 *
 * Variables de entorno:
 *   VIKUNJA_URL     (default http://127.0.0.1:3456)
 *   VIKUNJA_TOKEN   token de la API (si falta, se lee ~/.vikunja-token)
 *
 * Exit codes: 0 = sincronizado (o nada que hacer), 1 = error de red/auth/config.
 * Sin dependencias: solo módulos nativos de Node (>=18 con fetch).
 */

import { BASE_URL, apply, counts } from "./lib/board.mjs";

const log = (msg) => console.log(`[board:sync] ${msg}`);

function die(error) {
  const code = error?.code ? ` (${error.code})` : "";
  console.error(`[board:sync] ERROR${code}: ${error?.message ?? String(error)}`);
  process.exit(1);
}

const argv = process.argv.slice(2);
const DRY_RUN = argv.includes("--dry-run");

function onEvent(ev) {
  if (ev.type === "start") {
    log(`archivos fuente: ${ev.total} (${BASE_URL}${ev.dry_run ? ", dry-run" : ""})`);
  } else if (ev.type === "create") {
    log(`+ crear      ${ev.item.title}  -> ${ev.item.status}`);
  } else if (ev.type === "update") {
    log(`~ actualizar ${ev.item.title}  (${ev.how}): ${ev.changes.join(", ")}`);
  }
}

async function main() {
  const result = await apply({ dry_run: DRY_RUN, onEvent });

  log(
    `resumen: ${result.created} creadas, ${result.moved} movidas, ${result.updated} actualizadas, ` +
      `${result.unchanged} sin cambios, ${result.orphans.length} tarjetas huérfanas`,
  );

  if (result.orphans.length) {
    log("huérfanas (sin .md de origen; no se tocan):");
    for (const orphan of result.orphans) {
      log(`  · ${orphan.title} (${orphan.bucket}) [id ${orphan.card_id}]`);
    }
  }

  const perBucket = await counts();
  const summary = Object.entries(perBucket)
    .map(([bucket, n]) => `${bucket}=${n}`)
    .join(", ");
  log(`tarjetas por columna: ${summary || "sin tarjetas"}`);
}

main().catch(die);
