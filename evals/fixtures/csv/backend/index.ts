import express from 'express';
import { db } from './db';
const app=express();app.use(express.json());
app.get('/api/health',(_req,res)=>{res.json({ok:true});});
const bad=(v:unknown)=>typeof v!=='string'||!v.trim();

db.exec('CREATE TABLE IF NOT EXISTS datasets (id INTEGER PRIMARY KEY,name TEXT NOT NULL,columns TEXT NOT NULL,rows TEXT NOT NULL)');
function parse(csv:string) {
 const out:string[][]=[];let row:string[]=[],cell='',quoted=false,closed=false;
 for(let i=0;i<csv.length;i++){const c=csv[i];
  if(quoted){if(c==='"'){if(csv[i+1]==='"'){cell+='"';i++;}else{quoted=false;closed=true;}}else cell+=c;continue;}
  if(c==='"'){if(cell||closed)throw Error();quoted=true;}
  else if(c===','){row.push(cell);cell='';closed=false;}
  else if(c==='\n'||c==='\r'){if(c==='\r'&&csv[i+1]==='\n')i++;row.push(cell);out.push(row);row=[];cell='';closed=false;}
  else {if(closed)throw Error();cell+=c;}
 }
 if(quoted)throw Error();if(row.length||cell||closed){row.push(cell);out.push(row);}
 const [columns,...rows]=out;
 if(!columns||columns.some(bad)||new Set(columns).size!==columns.length||!rows.length||rows.some(r=>r.length!==columns.length))throw Error();
 return {columns,rows};
}
const decode=(r:any)=>({...r,columns:JSON.parse(r.columns),rows:JSON.parse(r.rows)});
app.get('/api/datasets',(_req,res)=>{res.json(db.prepare('SELECT * FROM datasets ORDER BY id').all().map(decode));});
app.post('/api/datasets',(req,res)=>{try{if(bad(req.body.name)||typeof req.body.csv!=='string')throw Error();const data=parse(req.body.csv);const id=Number(db.prepare('INSERT INTO datasets (name,columns,rows) VALUES (?,?,?)').run(req.body.name.trim(),JSON.stringify(data.columns),JSON.stringify(data.rows)).lastInsertRowid);res.status(201).json({id,name:req.body.name.trim(),...data});}catch{res.status(400).json({error:'Invalid CSV'});}});

app.listen(Number(process.env.BACKEND_PORT),'127.0.0.1');
