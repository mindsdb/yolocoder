// Runs in an evaluator-owned process. This source is never in the agent cwd.
import { chromium } from '@playwright/test';
import { writeFile, mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { checksFor } from './checks.mjs';

const [app,kind,url,controlUrl,output,seed='1'] = process.argv.slice(2);
if(!output) throw new Error('app kind app-url control-url output-dir seed required');
await mkdir(output,{recursive:true});
const browser=await chromium.launch({headless:true});
const context=await browser.newContext({viewport:{width:1280,height:900},locale:'en-GB',timezoneId:'UTC'});
const page=await context.newPage();page.setDefaultTimeout(3000);page.setDefaultNavigationTimeout(12000);
const errors=[];page.on('pageerror',e=>errors.push(e.message));
const tests=[];const results=[];
async function api(method,path,body) {
  const r=await fetch(url+path,{method,headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,signal:AbortSignal.timeout(10000)});
  const text=await r.text();let decoded;try{decoded=JSON.parse(text);}catch{decoded=text;}
  return {status:r.status,body:decoded};
}
async function restart() {
  const r=await fetch(controlUrl+'/process/restart',{method:'POST',signal:AbortSignal.timeout(30000)});
  if(!r.ok) throw new Error(`Restart failed: ${r.status}`);
  for(let i=0;i<100;i++) {try {if((await api('GET','/api/health')).status===200) return;}catch{} await new Promise(r=>setTimeout(r,100));}
  throw new Error('Server did not recover after restart');
}
checksFor(app,kind,{page,url,api,restart,seed,check:(id,group,critical,fn)=>tests.push({id,group,critical,fn})});
try {
  for(const test of tests) {
    const start=performance.now();const initialErrors=errors.length;
    try {await test.fn();if(errors.length>initialErrors) throw new Error(errors.slice(initialErrors).join('\n'));results.push({id:test.id,group:test.group,critical:test.critical,passed:true,duration_s:(performance.now()-start)/1000});}
    catch(error) {results.push({id:test.id,group:test.group,critical:test.critical,passed:false,duration_s:(performance.now()-start)/1000,error:String(error).slice(0,3000)});}
  }
  for(const [label,width,height] of [['mobile',390,844],['desktop',1280,900]]) {
    await page.setViewportSize({width,height});try{await page.goto(url,{waitUntil:'networkidle'});await page.screenshot({path:resolve(output,`${label}.png`),fullPage:true});}catch{}
  }
  await writeFile(resolve(output,'browser.json'),JSON.stringify({schema_version:1,checks:results,page_errors:errors,passed:results.length>0&&results.every(r=>r.passed)},null,2));
} finally {await browser.close();}
process.exitCode=results.every(r=>r.passed)?0:1;
