"""Reference feature implementations for offline grader calibration only."""
def apply(root,app,replace):
    def front(before,after):replace(root,'frontend/src/App.tsx',before,after)
    def back(before,after):replace(root,'backend/index.ts',before,after)
    if app=='csv':
        front('/*SORT_STATE*/',"const [column,K]=useState('None'),[direction,O]=useState('Ascending');")
        front('/*RESET_SORT*/',"K('None');O('Ascending');")
        front('/*SORT_ROWS*/',"""if(selected&&column!=='None'){const index=selected.columns.indexOf(column);const numeric=selected.rows.every(r=>r[index].trim()!==''&&Number.isFinite(Number(r[index])));visible.sort((a,b)=>(numeric?Number(a[index])-Number(b[index]):a[index]<b[index]?-1:a[index]>b[index]?1:0)*(direction==='Ascending'?1:-1));}""")
        front('{/*SORT_UI*/}',"""<label>Sort column<select value={column} onChange={e=>K(e.target.value)}><option>None</option>{selected.columns.map(c=><option key={c}>{c}</option>)}</select></label><label>Sort direction<select value={direction} onChange={e=>O(e.target.value)}><option>Ascending</option><option>Descending</option></select></label>""")
    elif app=='quiz':
        back('/*MIGRATE*/',"try{db.exec(\"ALTER TABLE questions ADD COLUMN explanation TEXT NOT NULL DEFAULT ''\");}catch{}")
        back('/*VALIDATE_EXPLANATION*/',"if(req.body.explanation!==undefined&&typeof req.body.explanation!=='string'){res.status(400).json({error:'Invalid explanation'});return;}")
        back('/*SAVE_EXPLANATION*/',"db.prepare('UPDATE questions SET explanation=? WHERE id=?').run(req.body.explanation??'',id);")
        front('/*EXPLANATION_STATE*/',"const [explanation,H]=useState('');")
        front('/*EXPLANATION_BODY*/',',explanation')
        front('{/*EXPLANATION_UI*/}','<label>Explanation<textarea value={explanation} onChange={e=>H(e.target.value)}/></label>')
        front('{/*EXPLANATION_REVEAL*/}','{score&&q.explanation&&<p>{q.explanation}</p>}')
    elif app=='inventory':
        front('/*FILTER_STATE*/',"const [low,L]=useState(false),[threshold,H]=useState('5');const valid=threshold.trim()!==''&&Number.isSafeInteger(Number(threshold))&&Number(threshold)>=0;")
        front('const visible=items;/*FILTER_ROWS*/','const visible=items.filter(i=>!low||!valid||i.quantity<=Number(threshold));')
        front('{/*FILTER_UI*/}','<label><input type="checkbox" checked={low} onChange={e=>L(e.target.checked)}/>Low stock only</label><label>Low stock threshold<input type="number" value={threshold} onChange={e=>H(e.target.value)}/></label>{!valid&&<p role="alert">Invalid threshold</p>}')
    elif app=='notes':
        back('/*MIGRATE*/',"try{db.exec('ALTER TABLE notes ADD COLUMN archived INTEGER NOT NULL DEFAULT 0');}catch{}")
        back('const decode=(r:any)=>r;/*DECODE*/','const decode=(r:any)=>({...r,archived:!!r.archived});')
        back('(title===undefined&&body===undefined)','(title===undefined&&body===undefined&&req.body.archived===undefined)')
        back("(body!==undefined&&typeof body!=='string')","(body!==undefined&&typeof body!=='string') || (req.body.archived!==undefined&&typeof req.body.archived!=='boolean')")
        back('/*SAVE_ARCHIVED*/',"if(req.body.archived!==undefined)db.prepare('UPDATE notes SET archived=? WHERE id=?').run(Number(req.body.archived),req.params.id);")
        front('/*VIEW_STATE*/',"const [view,V]=useState('Active');")
        front("notes.filter(n=>(n.title+' '+n.body)","notes.filter(n=>(view==='All'||(view==='Archived'?n.archived:!n.archived))&&(n.title+' '+n.body)")
        front('{/*VIEW_UI*/}','<label>Note view<select value={view} onChange={e=>V(e.target.value)}><option>Active</option><option>Archived</option><option>All</option></select></label>')
        front('{/*ARCHIVE_UI*/}',"""<button onClick={async()=>{await fetch(`/api/notes/${n.id}`,{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({archived:!n.archived})});await load();}}>{n.archived?'Restore':'Archive'} {n.title}</button>""")
    elif app=='application':
        back('/*DUPLICATE_ROUTE*/',"""app.post('/api/applications/:id/duplicate',(req,res)=>{const row=get(req.params.id);if(!row){res.status(404).json({error:'Missing application'});return;}if(row.status!=='submitted'){res.status(409).json({error:'Source is a draft'});return;}const id=db.prepare('INSERT INTO applications (name,email,track,statement) VALUES (?,?,?,?)').run(...fields.map(k=>row[k])).lastInsertRowid;res.status(201).json(get(id));});""")
        front('{/*DUPLICATE_UI*/}',"""<button onClick={async()=>{const response=await fetch(`/api/applications/${r.id}/duplicate`,{method:'POST'});if(!response.ok){E('Cannot duplicate');return;}const row:Row=await response.json();D(row);I(row.id);S(1);E('');T('');await load();}}>Duplicate {r.name}</button>""")
