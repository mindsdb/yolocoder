import express from 'express';
import { db } from './db';
const app=express();app.use(express.json());
app.get('/api/health',(_req,res)=>{res.json({ok:true});});
const bad=(v:unknown)=>typeof v!=='string'||!v.trim();

db.exec('CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY,title TEXT NOT NULL,body TEXT NOT NULL)');
/*MIGRATE*/
const decode=(r:any)=>r;/*DECODE*/
app.get('/api/notes',(_req,res)=>{res.json(db.prepare('SELECT * FROM notes ORDER BY id').all().map(decode));});
app.post('/api/notes',(req,res)=>{const {title,body}=req.body;if(bad(title)||typeof body!=='string'){res.status(400).json({error:'Invalid note'});return;}const id=db.prepare('INSERT INTO notes (title,body) VALUES (?,?)').run(title.trim(),body).lastInsertRowid;res.status(201).json(decode(db.prepare('SELECT * FROM notes WHERE id=?').get(id)));});
app.patch('/api/notes/:id',(req,res)=>{const old=db.prepare('SELECT * FROM notes WHERE id=?').get(req.params.id) as any;if(!old){res.status(404).json({error:'Missing note'});return;}
 const {title,body}=req.body;
 if((title===undefined&&body===undefined) || (title!==undefined&&bad(title)) || (body!==undefined&&typeof body!=='string')){res.status(400).json({error:'Invalid note'});return;}
 const nextTitle=title===undefined?old.title:title.trim();const nextBody=body===undefined?old.body:body;
 db.prepare('UPDATE notes SET title=?,body=? WHERE id=?').run(nextTitle,nextBody,req.params.id);/*SAVE_ARCHIVED*/
 res.json(decode(db.prepare('SELECT * FROM notes WHERE id=?').get(req.params.id)));
});

app.listen(Number(process.env.BACKEND_PORT),'127.0.0.1');
