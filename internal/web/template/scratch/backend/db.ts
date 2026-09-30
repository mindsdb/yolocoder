import path from "node:path";
import { fileURLToPath } from "node:url";
import Database from "better-sqlite3";
import { drizzle } from "drizzle-orm/better-sqlite3";
import { sqliteTable, integer, text } from "drizzle-orm/sqlite-core";

export const notes = sqliteTable("notes", {
  id: integer("id").primaryKey({ autoIncrement: true }),
  text: text("text").notNull(),
  createdAt: integer("created_at", { mode: "timestamp" }).notNull(),
});

// Resolved from this file's own location (not process.cwd()) so the
// database always lands next to it, in backend/, regardless of where
// the process was started from.
const dbPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "data.db");

const sqlite = new Database(dbPath);

// Each table is written twice: once above for Drizzle's types and queries,
// and once below as the SQL that creates it. Nothing runs drizzle-kit on
// start, so a table added above and not here does not exist at runtime,
// and the type check cannot tell. Add, rename or change a column in both.
sqlite.exec(`
  CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    text TEXT NOT NULL,
    created_at INTEGER NOT NULL
  )
`);

export const db = drizzle(sqlite, { schema: { notes } });
