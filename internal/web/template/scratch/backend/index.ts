import express from "express";

const app = express();
app.use(express.json());

app.get("/api/hello", (_req, res) => {
  res.json({ message: "Hello from Express." });
});

const port = Number(process.env.PORT ?? 3001);
app.listen(port, () => {
  console.log(`API listening on http://localhost:${port}`);
});
