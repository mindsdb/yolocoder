import express from "express";
import { db } from "./db/client";
import { notes } from "./db/schema";

const app = express();
app.use(express.json());

app.get("/api/hello", (_req, res) => {
  res.json({ message: "Hello from Express + Drizzle + SQLite." });
});

app.get("/api/notes", async (_req, res) => {
  res.json(await db.select().from(notes).all());
});

app.post("/api/notes", async (req, res) => {
  const text = String(req.body?.text ?? "").trim();
  if (!text) {
    res.status(400).json({ error: "text is required" });
    return;
  }
  await db.insert(notes).values({ text, createdAt: new Date() });
  res.status(201).json({ ok: true });
});

const port = Number(process.env.PORT ?? 3001);
app.listen(port, () => {
  console.log(`API listening on http://localhost:${port}`);
});
