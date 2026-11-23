/**
 * scripts/lib/board.mjs — espejo md -> tablero Vikunja para Ferry.
 *
 * Regla del repo: **el .md es la fuente de verdad; el tablero es un espejo**.
 * Esta librería solo empuja estado desde `docs/tasks/*.md` al tablero; nunca
 * escribe en los .md ni borra tarjetas. Es idempotente: la segunda pasada debe
 * reportar 0 cambios.
 *
 * Contrato:
 *   buildDesired()            -> [{anchor, title, status, priority, tipo, tags, summary}]
 *   fetchState()              -> [card] (con bucketId y bucketTitle)
 *   diff(state, desired)      -> {create[], move[], update[], unchanged, orphans[]}
 *   apply({dry_run,onEvent})  -> {created, moved, updated, unchanged, orphans}
 *   counts()                  -> {bucketTitle: n}
 *   orphans()                 -> [{card_id, title, bucket}]
 *
 * Determinismo: ids fijos (constante BOARD), listas ordenadas por ruta o id y
 * sin depender del orden de readdir.
 *
 * Variables:
 *   VIKUNJA_URL     (default http://127.0.0.1:3456)
 *   VIKUNJA_TOKEN   token de la API (si falta, se lee ~/.vikunja-token)
 *
 * Sin dependencias: solo módulos nativos de Node (>=18 con fetch).
 */

import { readFileSync, readdirSync, existsSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/** Raíz del repo: scripts/lib/ -> ../.. */
export const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

/** URL del tablero, sin barra final. */
export const BASE_URL = (process.env.VIKUNJA_URL ?? "http://127.0.0.1:3456").replace(/\/+$/, "");

/**
 * Proyecto único del board. Estos ids NO se redescubren en cada ejecución:
 * projectId=6 "ferry / tareas y fixes", vista kanban 35 y sus tres columnas.
 */
export const BOARD = {
  projectId: 6,
  viewId: 35,
  buckets: { "por hacer": 40, "en progreso": 41, completado: 42 },
};

/** Prioridad de Vikunja: alta=3, media=2, baja=1. Sin prioridad no se escribe. */
const PRIORITY = { alta: 3, media: 2, baja: 1 };

/** Error con código estable para el CLI. */
export class BoardError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "BoardError";
    this.code = code;
  }

  toJSON() {
    return { code: this.code, message: this.message };
  }
}

function token() {
  if (process.env.VIKUNJA_TOKEN) return process.env.VIKUNJA_TOKEN.trim();
  const file = join(homedir(), ".vikunja-token");
  if (existsSync(file)) return readFileSync(file, "utf8").trim();
  throw new BoardError("TOKEN_INVALID", "falta VIKUNJA_TOKEN y no existe ~/.vikunja-token");
}

async function api(method, path, body) {
  const bearer = token();

  let res;
  try {
    res = await fetch(BASE_URL + path, {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${bearer}`,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(15000),
    });
  } catch (e) {
    throw new BoardError(
      "BOARD_UNREACHABLE",
      `no se pudo alcanzar Vikunja en ${BASE_URL} (${e.message}). ` +
        `¿Está arriba? cd ~/stacks/vikunja && docker compose up -d`,
    );
  }

  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    /* respuesta no JSON: se devuelve el texto crudo en el error */
  }

  if (!res.ok) {
    const detail = json && typeof json === "object" ? JSON.stringify(json) : text.slice(0, 200);
    const message = `${method} ${path} -> HTTP ${res.status} ${detail}`;
    if (res.status === 401 || res.status === 403) throw new BoardError("TOKEN_INVALID", message);
    if (res.status >= 500) throw new BoardError("BOARD_UNREACHABLE", message);
    throw new BoardError("HTTP_ERROR", message);
  }
  return json;
}

/* ------------------------------ lectura de md ------------------------------ */

function frontmatter(text) {
  if (!text.startsWith("---")) return {};
  const end = text.indexOf("\n---", 3);
  if (end === -1) return {};
  const out = {};
  for (const raw of text.slice(3, end).split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const i = line.indexOf(":");
    if (i === -1) continue;
    const key = line.slice(0, i).trim();
    const value = line.slice(i + 1).trim();
    if (value.startsWith("[") && value.endsWith("]")) {
      out[key] = value
        .slice(1, -1)
        .split(",")
        .map((v) => v.trim().replace(/^["']|["']$/g, ""))
        .filter(Boolean);
    } else {
      out[key] = value.replace(/^["']|["']$/g, "");
    }
  }
  return out;
}

function firstHeading(text) {
  const m = text.match(/^#\s+(.+)$/m);
  return m ? m[1].trim() : null;
}

function normalizeStatus(raw) {
  const s = String(raw ?? "")
    .toLowerCase()
    .replace(/[-_]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (["completado", "completada", "hecho", "hecha", "done", "cerrado"].includes(s)) {
    return "completado";
  }
  if (["en progreso", "en curso", "wip", "doing", "en revision"].includes(s)) {
    return "en progreso";
  }
  if (["por hacer", "pendiente", "todo", "backlog", "abierto"].includes(s)) {
    return "por hacer";
  }
  return null;
}

function normalizeTipo(raw) {
  const s = String(raw ?? "").trim().toLowerCase();
  return s === "fix" ? "fix" : "todo";
}

function tagList(raw) {
  if (Array.isArray(raw)) return raw.map((v) => String(v).trim()).filter(Boolean);
  return String(raw ?? "")
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
}

function summarize(text, max = 420) {
  // Primer bloque sustantivo del cuerpo, sin frontmatter, encabezados ni tablas.
  const body = text.replace(/^---[\s\S]*?\n---/, "");
  for (const block of body.split(/\r?\n\s*\r?\n/)) {
    const clean = block
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#") && !line.startsWith("|") && !line.startsWith(">"));
    if (!clean.length) continue;
    const s = clean.join(" ").replace(/\s+/g, " ").trim();
    if (!s) continue;
    return s.length > max ? `${s.slice(0, max - 1)}…` : s;
  }
  return "";
}

function taskFiles() {
  const abs = join(REPO_ROOT, "docs/tasks");
  if (!existsSync(abs)) return [];
  return readdirSync(abs)
    .filter((f) => f.endsWith(".md") && f.toLowerCase() !== "readme.md")
    .sort()
    .map((f) => join("docs/tasks", f));
}

/* --------------------------- estado deseado --------------------------- */

/** Qué debería haber en el tablero según `docs/tasks/*.md`, ordenado por ruta. */
export function buildDesired() {
  const items = [];
  for (const anchor of taskFiles()) {
    const text = readFileSync(join(REPO_ROOT, anchor), "utf8");
    const fm = frontmatter(text);
    items.push({
      anchor,
      title: firstHeading(text) ?? basename(anchor, ".md"),
      status: normalizeStatus(fm.status) ?? "por hacer",
      priority: PRIORITY[String(fm.prioridad ?? "").toLowerCase()] ?? 0,
      tipo: normalizeTipo(fm.tipo),
      tags: tagList(fm.tags),
      summary: summarize(text),
    });
  }
  return items.sort((a, b) => a.anchor.localeCompare(b.anchor));
}

/* ------------------------------ emparejado ------------------------------ */

const normalizeTitle = (s) =>
  String(s)
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9 ]+/g, " ")
    .replace(/\s+/g, " ")
    .trim();

function anchorOf(description) {
  const m = String(description ?? "").match(/Ruta:\s*(docs\/tasks\/[\w.-]+\.md)/);
  return m ? m[1] : null;
}

function tipoOf(description) {
  const m = String(description ?? "").match(/^Tipo:\s*(\S+)/m);
  return m ? m[1].toLowerCase() : null;
}

function tagsOf(description) {
  const m = String(description ?? "").match(/^Tags:\s*(.*)$/m);
  return m ? m[1].trim() : "";
}

function findCard(item, cards, used) {
  const free = cards.filter((card) => !used.has(card.id));
  const byAnchor = free.find((card) => anchorOf(card.description) === item.anchor);
  if (byAnchor) return { card: byAnchor, how: "ancla" };

  const norm = normalizeTitle(item.title);
  const byTitle = free.find((card) => normalizeTitle(card.title) === norm);
  if (byTitle) return { card: byTitle, how: "título" };

  return null;
}

/* -------------------------------- tablero -------------------------------- */

/** Estado actual del tablero, con el bucket de cada tarjeta. */
export async function fetchState() {
  const buckets = await api(
    "GET",
    `/api/v1/projects/${BOARD.projectId}/views/${BOARD.viewId}/tasks?per_page=100`,
  );
  const cards = [];
  for (const bucket of buckets ?? []) {
    for (const task of bucket.tasks ?? []) {
      cards.push({ ...task, bucketId: bucket.id, bucketTitle: bucket.title });
    }
  }
  cards.sort((a, b) => String(a.id).localeCompare(String(b.id)));
  return cards;
}

function descriptionFor(item) {
  return [
    `Ruta: ${item.anchor}`,
    `Tipo: ${item.tipo}`,
    item.tags.length ? `Tags: ${item.tags.join(", ")}` : "",
    item.summary,
  ]
    .filter(Boolean)
    .join("\n\n");
}

function bucketIdFor(item) {
  return BOARD.buckets[item.status] ?? BOARD.buckets["por hacer"];
}

/** Cambios necesarios para que una tarjeta refleje su .md (vacío = al día). */
function changesFor(item, card, bucketId) {
  const changes = [];
  const current = String(card.description ?? "");
  const wanted = descriptionFor(item);
  if (anchorOf(current) !== item.anchor) changes.push("ancla");
  if (tipoOf(current) !== item.tipo) changes.push("tipo");
  if (tagsOf(current) !== item.tags.join(", ")) changes.push("tags");
  // Un movimiento u otro cambio tampoco puede dejar la descripción a medias:
  // si el texto completo no coincide, se reescribe en la misma pasada.
  if (current !== wanted && changes.length === 0) changes.push("descripción");
  if (card.bucketId !== bucketId) {
    changes.push(`bucket ${card.bucketTitle ?? card.bucketId} -> ${item.status}`);
  }
  if (normalizeTitle(card.title) !== normalizeTitle(item.title)) changes.push("título");
  // Sin prioridad en el .md no se degrada lo que ya tenga la tarjeta.
  if (item.priority > 0 && (card.priority ?? 0) !== item.priority) {
    changes.push(`prioridad ${card.priority ?? 0} -> ${item.priority}`);
  }
  return changes;
}

function sortOrphans(list) {
  return list.sort(
    (a, b) =>
      String(a.title ?? "").localeCompare(String(b.title ?? "")) ||
      String(a.card_id ?? "").localeCompare(String(b.card_id ?? "")),
  );
}

function orphansOf(state, used) {
  const out = [];
  for (const card of state) {
    if (used.has(card.id)) continue;
    out.push({ card_id: card.id, title: card.title, bucket: card.bucketTitle });
  }
  return sortOrphans(out);
}

/** Diff puro (sin escrituras) entre el estado del tablero y los .md. */
export function diff(state, desired = buildDesired()) {
  const out = { create: [], move: [], update: [], unchanged: 0, orphans: [] };
  const used = new Set();

  for (const item of desired) {
    const bucketId = bucketIdFor(item);
    const match = findCard(item, state, used);

    if (!match) {
      out.create.push({
        anchor: item.anchor,
        title: item.title,
        status: item.status,
        bucket: item.status,
        priority: item.priority,
      });
      continue;
    }

    const card = match.card;
    used.add(card.id);
    const changes = changesFor(item, card, bucketId);
    if (changes.length === 0) {
      out.unchanged += 1;
      continue;
    }

    const entry = {
      anchor: item.anchor,
      task_id: card.id,
      title: item.title,
      from_bucket: card.bucketTitle,
      to_bucket: item.status,
      how: match.how,
      changes,
    };
    if (changes.some((c) => c.startsWith("bucket"))) out.move.push(entry);
    else out.update.push(entry);
  }

  out.orphans = orphansOf(state, used);
  return out;
}

function patchFor(item, card) {
  // Vikunja reemplaza los campos que recibe en la actualización de una tarea.
  // La descripción completa viaja en CUALQUIER cambio (crear ya la escribe,
  // mover pasa por aquí): así el ancla, el tipo y los tags nunca se pierden y
  // la segunda pasada no tiene nada que reparar.
  const patch = { title: item.title, description: descriptionFor(item) };
  // Una tarjeta sin prioridad en el .md conserva la que ya tenía.
  patch.priority = item.priority > 0 ? item.priority : (card.priority ?? 0);
  // Si el título solo difiere en mayúsculas/acentos, no se reescribe.
  if (normalizeTitle(card.title) === normalizeTitle(item.title) && card.title === item.title) {
    delete patch.title;
  }
  return patch;
}

/**
 * Aplica el plan: crea lo que falta, mueve de columna y actualiza
 * título/prioridad/descripción. **Nunca borra tarjetas.** `dry_run` recorre el
 * mismo plan sin escribir.
 */
export async function apply({ dry_run = false, onEvent } = {}) {
  const desired = buildDesired();
  const emit = typeof onEvent === "function" ? onEvent : () => {};
  emit({ type: "start", total: desired.length, dry_run });

  const state = await fetchState();
  const stats = { created: 0, moved: 0, updated: 0, unchanged: 0 };
  const used = new Set();

  for (const item of desired) {
    const bucketId = bucketIdFor(item);
    const description = descriptionFor(item);
    const match = findCard(item, state, used);

    if (!match) {
      stats.created += 1;
      emit({ type: "create", item });
      if (!dry_run) {
        const created = await api("PUT", `/api/v1/projects/${BOARD.projectId}/tasks`, {
          title: item.title,
          description,
          ...(item.priority > 0 ? { priority: item.priority } : {}),
        });
        used.add(created.id);
        await api(
          "POST",
          `/api/v1/projects/${BOARD.projectId}/views/${BOARD.viewId}/buckets/${bucketId}/tasks`,
          { task_id: created.id },
        );
      }
      continue;
    }

    const card = match.card;
    used.add(card.id);
    const changes = changesFor(item, card, bucketId);
    if (changes.length === 0) {
      stats.unchanged += 1;
      continue;
    }

    emit({ type: "update", item, how: match.how, changes });
    if (changes.some((c) => c.startsWith("bucket"))) stats.moved += 1;
    else stats.updated += 1;

    if (!dry_run) {
      await api("POST", `/api/v1/tasks/${card.id}`, patchFor(item, card));
      if (card.bucketId !== bucketId) {
        await api(
          "POST",
          `/api/v1/projects/${BOARD.projectId}/views/${BOARD.viewId}/buckets/${bucketId}/tasks`,
          { task_id: card.id },
        );
      }
    }
  }

  return { ...stats, orphans: orphansOf(state, used) };
}

/** Tarjetas del tablero sin .md de origen (nunca se tocan). */
export async function orphans() {
  const desired = buildDesired();
  const state = await fetchState();
  const used = new Set();
  for (const item of desired) {
    const match = findCard(item, state, used);
    if (match) used.add(match.card.id);
  }
  return orphansOf(state, used);
}

/** Cuántas tarjetas hay en cada columna del board. */
export async function counts() {
  const state = await fetchState();
  const perBucket = {};
  for (const card of state) {
    perBucket[card.bucketTitle] = (perBucket[card.bucketTitle] ?? 0) + 1;
  }
  return Object.fromEntries(Object.keys(perBucket).sort().map((b) => [b, perBucket[b]]));
}
