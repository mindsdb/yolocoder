import { defineConfig } from "drizzle-kit";

export default defineConfig({
  schema: "./backend/db.ts",
  out: "./backend/drizzle",
  dialect: "sqlite",
  dbCredentials: {
    url: "./backend/data.db",
  },
});
