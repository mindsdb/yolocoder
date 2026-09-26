import express from 'express';
const app = express();
app.use(express.json());
app.get('/api/health', (_req, res) => { res.json({ ok: true }); });
app.listen(Number(process.env.BACKEND_PORT), '127.0.0.1');
