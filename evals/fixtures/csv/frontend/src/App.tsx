import {useState,useEffect} from 'react';
type Dataset={id:number;name:string;columns:string[];rows:string[][]};
export default function App(){const [sets,S]=useState<Dataset[]>([]),[selected,A]=useState<Dataset|null>(null),[name,N]=useState(''),[csv,C]=useState(''),[query,Q]=useState(''),[error,E]=useState('');
 const load=()=>fetch('/api/datasets').then(r=>r.json()).then(S);useEffect(()=>{void load();},[]);
 function open(d:Dataset){A(d);Q('');/*RESET_SORT*/}
 async function add(e:React.FormEvent){e.preventDefault();const r=await fetch('/api/datasets',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name,csv})});if(!r.ok){E('Invalid CSV');return;}E('');open(await r.json());await load();}
 /*SORT_STATE*/
 const visible=(selected?.rows??[]).filter(row=>row.some(cell=>cell.toLowerCase().includes(query.toLowerCase())));
 /*SORT_ROWS*/
 return <main><h1>CSV explorer</h1><form onSubmit={add}><label>Dataset name<input value={name} onChange={e=>N(e.target.value)}/></label><label>CSV<textarea value={csv} onChange={e=>C(e.target.value)}/></label><button>Import CSV</button></form><p role="alert">{error}</p><ul>{sets.map(d=><li key={d.id}><button onClick={()=>open(d)}>Open {d.name}</button></li>)}</ul>{selected&&<><label>Search rows<input value={query} onChange={e=>Q(e.target.value)}/></label>{/*SORT_UI*/}<table aria-label="Data"><thead><tr>{selected.columns.map(c=><th key={c}>{c}</th>)}</tr></thead><tbody>{visible.map((row,i)=><tr key={i}>{row.map((cell,j)=><td key={j}>{cell}</td>)}</tr>)}</tbody></table></>}</main>;
}
