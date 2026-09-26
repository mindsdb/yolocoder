import {useState,useEffect} from 'react';
type Item={id:number;sku:string;name:string;quantity:number};
export default function App(){const [items,S]=useState<Item[]>([]),[sku,K]=useState(''),[name,N]=useState(''),[quantity,Q]=useState('0'),[delta,D]=useState<Record<number,string>>({}),[error,E]=useState('');/*FILTER_STATE*/
 const load=()=>fetch('/api/items').then(r=>r.json()).then(S);useEffect(()=>{void load();},[]);
 async function add(e:React.FormEvent){e.preventDefault();const r=await fetch('/api/items',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sku,name,quantity:Number(quantity)})});if(!r.ok){E('Invalid or duplicate item');return;}E('');K('');N('');await load();}
 async function adjust(i:Item){const r=await fetch(`/api/items/${i.id}/adjust`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({delta:Number(delta[i.id])})});E(r.ok?'':'Adjustment refused');await load();}
 const visible=items;/*FILTER_ROWS*/
 return <main><h1>Inventory manager</h1><form onSubmit={add}><label>SKU<input value={sku} onChange={e=>K(e.target.value)}/></label><label>Item name<input value={name} onChange={e=>N(e.target.value)}/></label><label>Quantity<input type="number" value={quantity} onChange={e=>Q(e.target.value)}/></label><button>Add item</button></form><p role="alert">{error}</p>{/*FILTER_UI*/}<table aria-label="Inventory"><thead><tr><th>SKU</th><th>Name</th><th>Quantity</th><th>Adjust</th></tr></thead><tbody>{visible.map(i=><tr key={i.id}><td>{i.sku}</td><td>{i.name}</td><td>{i.quantity}</td><td><label>Adjustment {i.sku}<input type="number" value={delta[i.id]??''} onChange={e=>D({...delta,[i.id]:e.target.value})}/></label><button onClick={()=>void adjust(i)}>Adjust {i.sku}</button></td></tr>)}</tbody></table></main>;
}
