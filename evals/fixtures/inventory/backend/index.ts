import express from 'express';
import { db } from './db';
const app=express();app.use(express.json());
app.get('/api/health',(_req,res)=>{res.json({ok:true});});
const bad=(v:unknown)=>typeof v!=='string'||!v.trim();

db.exec('CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY,sku TEXT NOT NULL COLLATE NOCASE UNIQUE,name TEXT NOT NULL,quantity INTEGER NOT NULL)');
app.get('/api/items',(_req,res)=>{res.json(db.prepare('SELECT * FROM items ORDER BY id').all());});
app.post('/api/items',(req,res)=>{const {sku,name,quantity}=req.body;if(bad(sku)||bad(name)||!Number.isSafeInteger(quantity)||quantity<0){res.status(400).json({error:'Invalid item'});return;}if(db.prepare('SELECT id FROM items WHERE sku=? COLLATE NOCASE').get(sku.trim())){res.status(409).json({error:'Duplicate SKU'});return;}const id=db.prepare('INSERT INTO items (sku,name,quantity) VALUES (?,?,?)').run(sku.trim(),name.trim(),quantity).lastInsertRowid;res.status(201).json(db.prepare('SELECT * FROM items WHERE id=?').get(id));});
const adjust=db.transaction((id:string,delta:number)=>{const row=db.prepare('SELECT * FROM items WHERE id=?').get(id) as any;if(!row)return {status:404};const quantity=row.quantity+delta;if(quantity<0||!Number.isSafeInteger(quantity))return {status:409};db.prepare('UPDATE items SET quantity=? WHERE id=?').run(quantity,id);return {status:200,row:{...row,quantity}};});
app.post('/api/items/:id/adjust',(req,res)=>{const delta=req.body.delta;if(!Number.isSafeInteger(delta)||delta===0){res.status(400).json({error:'Invalid adjustment'});return;}const r=adjust(req.params.id,delta);res.status(r.status).json(r.row??{error:'Adjustment refused'});});

app.listen(Number(process.env.BACKEND_PORT),'127.0.0.1');
