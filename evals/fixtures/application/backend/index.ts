import express from 'express';
import { db } from './db';
const app=express();app.use(express.json());
app.get('/api/health',(_req,res)=>{res.json({ok:true});});
const bad=(v:unknown)=>typeof v!=='string'||!v.trim();

db.exec("CREATE TABLE IF NOT EXISTS applications (id INTEGER PRIMARY KEY,name TEXT NOT NULL,email TEXT NOT NULL,track TEXT NOT NULL,statement TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'draft')");
const fields=['name','email','track','statement'];const strings=(b:any)=>fields.every(k=>typeof b[k]==='string');
const get=(id:any)=>db.prepare('SELECT * FROM applications WHERE id=?').get(id) as any;
app.get('/api/applications',(_req,res)=>{res.json(db.prepare('SELECT * FROM applications ORDER BY id').all());});
app.post('/api/applications',(req,res)=>{if(!strings(req.body)){res.status(400).json({error:'Invalid fields'});return;}const id=db.prepare('INSERT INTO applications (name,email,track,statement) VALUES (?,?,?,?)').run(...fields.map(k=>req.body[k])).lastInsertRowid;res.status(201).json(get(id));});
app.put('/api/applications/:id',(req,res)=>{const row=get(req.params.id);if(!row){res.status(404).json({error:'Missing application'});return;}if(row.status==='submitted'){res.status(409).json({error:'Already submitted'});return;}if(!strings(req.body)){res.status(400).json({error:'Invalid fields'});return;}db.prepare('UPDATE applications SET name=?,email=?,track=?,statement=? WHERE id=?').run(...fields.map(k=>req.body[k]),req.params.id);res.json(get(req.params.id));});
app.post('/api/applications/:id/submit',(req,res)=>{const row=get(req.params.id);if(!row){res.status(404).json({error:'Missing application'});return;}if(row.status==='submitted'){res.status(409).json({error:'Already submitted'});return;}if(bad(row.name)||!/^\S+@\S+\.\S+$/.test(row.email.trim())||!['Design','Engineering'].includes(row.track)||row.statement.trim().length<10){res.status(400).json({error:'Incomplete application'});return;}db.prepare("UPDATE applications SET status='submitted' WHERE id=?").run(req.params.id);res.json(get(req.params.id));});
/*DUPLICATE_ROUTE*/

app.listen(Number(process.env.BACKEND_PORT),'127.0.0.1');
