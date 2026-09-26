import assert from 'node:assert/strict';
import {test} from 'node:test';
import {chromium} from '@playwright/test';
import {control} from '../browser/checks.mjs';

test('text-entry controls accept valid native roles and label forms',async()=>{
 const browser=await chromium.launch();const page=await browser.newPage();
 try{
  for(const type of ['text','search','number','email','url','tel','password']){
   for(const wrapping of [true,false]){
    const input=`<input id="field" type="${type}">`;
    await page.setContent(wrapping?`<label>Search notes${input}</label>`:`<label for="field">Search notes</label>${input}`);
    const locator=control(page,'Search notes');assert.equal(await locator.count(),1,type);
    await locator.fill(type==='number'?'17':'changed');assert.equal(await locator.inputValue(),type==='number'?'17':'changed');
   }
  }
  await page.setContent('<label>Note body<textarea>Existing text</textarea></label>');
  await control(page,'Note body').fill('Changed\nbody');assert.equal(await control(page,'Note body').inputValue(),'Changed\nbody');
  await page.setContent('<input type="search" aria-label="Search rows">');
  await control(page,'Search rows').fill('Alpha');assert.equal(await control(page,'Search rows').inputValue(),'Alpha');
 }finally{await browser.close();}
});

test('selects and checkboxes remain unambiguous with wrapping labels',async()=>{
 const browser=await chromium.launch();const page=await browser.newPage();
 try{
  await page.setContent('<label>Correct answer<select><option value="0">A</option><option value="1">B</option></select></label><label><input type="checkbox">Low stock only</label>');
  assert.equal(await control(page,'Correct answer').count(),1);await control(page,'Correct answer').selectOption({label:'B'});assert.equal(await control(page,'Correct answer').inputValue(),'1');
  await control(page,'Low stock only').check();assert.equal(await control(page,'Low stock only').isChecked(),true);
  assert.equal(await control(page,'Missing label').count(),0);
 }finally{await browser.close();}
});
