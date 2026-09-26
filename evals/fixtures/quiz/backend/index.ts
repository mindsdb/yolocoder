import express from 'express';
import { db } from './db';
const app=express();app.use(express.json());
app.get('/api/health',(_req,res)=>{res.json({ok:true});});
const bad=(v:unknown)=>typeof v!=='string'||!v.trim();

db.exec('CREATE TABLE IF NOT EXISTS questions (id INTEGER PRIMARY KEY,prompt TEXT NOT NULL,options TEXT NOT NULL,correctIndex INTEGER NOT NULL)');
/*MIGRATE*/
const decode=(r:any)=>({...r,options:JSON.parse(r.options)});
app.get('/api/questions',(_req,res)=>{res.json(db.prepare('SELECT * FROM questions ORDER BY id').all().map(decode));});
app.post('/api/questions',(req,res)=>{const {prompt,options,correctIndex}=req.body;
 if(bad(prompt)||!Array.isArray(options)||options.length!==2||options.some(bad)||![0,1].includes(correctIndex)){res.status(400).json({error:'Invalid question'});return;}
 /*VALIDATE_EXPLANATION*/
 const id=Number(db.prepare('INSERT INTO questions (prompt,options,correctIndex) VALUES (?,?,?)').run(prompt.trim(),JSON.stringify(options.map((o:string)=>o.trim())),correctIndex).lastInsertRowid);
 /*SAVE_EXPLANATION*/
 res.status(201).json(decode(db.prepare('SELECT * FROM questions WHERE id=?').get(id)));
});
app.post('/api/grade',(req,res)=>{const answers=req.body.answers;
 if(!Array.isArray(answers)){res.status(400).json({error:'Invalid answers'});return;}
 const ids=new Set<number>();let correct=0;
 for(const a of answers){if(!a||![0,1].includes(a.choice)||!Number.isSafeInteger(a.id)||ids.has(a.id)){res.status(400).json({error:'Invalid answer'});return;}ids.add(a.id);const q=db.prepare('SELECT * FROM questions WHERE id=?').get(a.id) as any;if(!q){res.status(400).json({error:'Unknown question'});return;}if(a.choice===q.correctIndex)correct++;}
 res.json({correct,total:answers.length});
});

app.listen(Number(process.env.BACKEND_PORT),'127.0.0.1');
