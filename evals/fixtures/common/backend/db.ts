import Database from 'better-sqlite3';
import { fileURLToPath } from 'node:url';
export const db = new Database(fileURLToPath(new URL('./data.db', import.meta.url)));
db.pragma('journal_mode = WAL');
