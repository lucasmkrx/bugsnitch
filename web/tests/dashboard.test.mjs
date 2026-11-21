// SPDX-License-Identifier: MPL-2.0
// Browser protocol fixtures. All records in this suite are synthetic.
import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile,mkdir} from 'node:fs/promises';
import {resolve,extname} from 'node:path';
import {chromium} from 'playwright-core';

const staticRoot=resolve('dist');
const types={'.html':'text/html','.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml'};
async function staticHost(){
 const server=createServer(async(req,res)=>{
  const path=new URL(req.url,'http://local').pathname;
  const file=resolve(staticRoot,'.'+(path==='/'?'/index.html':path));
  if(!file.startsWith(staticRoot+'/')){res.writeHead(404).end();return}
  try{const body=await readFile(file);res.writeHead(200,{'content-type':types[extname(file)]||'application/octet-stream'}).end(body)}catch{res.writeHead(404).end()}
 });
 await new Promise((ok,fail)=>server.once('error',fail).listen(0,'127.0.0.1',ok));
 return {server,url:'http://127.0.0.1:'+server.address().port};
}
const project={id:'p1',name:'Storefront',repository:'example/storefront',paused:true,version:4};
const decision={id:'d1',projectId:'p1',subjectVersion:4,version:1,status:'pending',question:'Allow this project to accept new work again?',options:['Resume dispatch','Keep paused'],expiresAt:'2026-12-01T18:00:00Z',answer:null,requestId:null};
function fixture(){return {workspace:{id:'w1',name:'Test workspace'},projects:[{...project}],devices:[],incidents:[],decisions:[{...decision}],attempts:[],tickets:[],plans:[],pullRequests:[],messages:[],events:[{seq:1,projectId:'p1',kind:'project.created',subjectId:'p1',createdAt:'2026-11-21T14:03:00Z'}],capabilities:{hostedInference:false,reason:'Hosted inference requires deployment authorization.'},updatedAt:'2026-11-21T15:06:00Z'}}
test('desktop and mobile approval, stale state, replay and owner navigation',async t=>{
 const {server,url}=await staticHost();t.after(()=>server.close());
 const browser=await chromium.launch(process.env.BUGSNITCH_TEST_CHROME?{executablePath:process.env.BUGSNITCH_TEST_CHROME,headless:true}:{channel:'chrome',headless:true});
 t.after(()=>browser.close());
 for(const viewport of [{width:1440,height:900},{width:360,height:800}]){
  await t.test(viewport.width+'px viewport',async()=>{
   const c=await browser.newContext({viewport});try{
    let state=fixture(),mode='normal',answers=[];
    const page=await c.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.route('**/api/v1/**',async route=>{
     const req=route.request(),path=new URL(req.url()).pathname;
     if(path.endsWith('/state'))return route.fulfill({json:state});
     if(path.endsWith('/answer')){
      const body=req.postDataJSON();answers.push(body);
      if(mode==='lose-request'){mode='normal';return route.abort('connectionreset')}
      Object.assign(state.decisions[0],{answer:body.answer,requestId:body.requestId,status:'answered',version:2});return route.fulfill({json:{id:'d1',answer:body.answer}});
     }
     if(path.endsWith('/pairings'))return route.fulfill({status:201,json:{code:'0123456789abcdef',expiresAt:'2026-11-21T15:16:00Z'}});
     return route.fulfill({status:404,json:{error:'Unconfigured test route'}});
    });
    await page.goto(url);await page.getByRole('heading',{name:'Overview',exact:true}).waitFor();
    if(process.env.BUGSNITCH_SCREENSHOTS){await mkdir(process.env.BUGSNITCH_SCREENSHOTS,{recursive:true});await page.screenshot({path:resolve(process.env.BUGSNITCH_SCREENSHOTS,'fixture-overview-'+viewport.width+'.png')})}
    await page.keyboard.press('Tab');assert.equal(await page.locator(':focus').innerText(),'Skip to content');await page.keyboard.press('Enter');assert.equal(await page.locator(':focus').getAttribute('id'),'main-content');
    const noOverflow=async()=>assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'horizontal page overflow');
    await noOverflow();
    if(viewport.width===360){await page.getByRole('button',{name:'Open navigation'}).click();await page.getByRole('dialog').getByRole('link',{name:/Decisions/}).click()}
    else await page.getByRole('navigation').getByRole('link',{name:/Decisions/}).click();
    await page.getByRole('button',{name:'Keep paused',exact:true}).waitFor();
    // The open tab still has the old record. A fresh read on click must prevent this POST.
    state.decisions[0].status='stale';
    await page.getByRole('button',{name:'Keep paused',exact:true}).click();
    await page.getByRole('alert').filter({hasText:'This decision changed'}).waitFor();
    assert.equal(answers.length,0);
    state=fixture();await page.reload();await page.getByRole('button',{name:'Keep paused',exact:true}).waitFor();
    await c.setOffline(true);await page.getByText(/Offline. Displaying the last received workspace state/).waitFor();
    assert.equal(await page.getByRole('button',{name:'Keep paused',exact:true}).isDisabled(),true);
    await c.setOffline(false);await page.waitForFunction(()=>[...document.querySelectorAll('button')].some(b=>b.textContent==='Keep paused'&&!b.disabled));
    // Delivery failed before the service committed. An explicit retry must reuse the same ID.
    mode='lose-request';await page.getByRole('button',{name:'Keep paused',exact:true}).click();
    await page.getByRole('alert').filter({hasText:'service could not be reached'}).waitFor();
    assert.equal(answers.length,1);
    await page.getByRole('button',{name:'Keep paused',exact:true}).click();
    await page.getByText('Recorded answer · Keep paused').waitFor();
    assert.equal(answers.length,2);assert.equal(answers[0].requestId,answers[1].requestId);
    // A completed answer is read back after reload without replaying a mutation.
    await page.reload();await page.getByText('Recorded answer · Keep paused').waitFor();assert.equal(answers.length,2);
    for(const section of ['plans','work','prs','incidents','devices','chat','settings']){
     await page.goto(url+'/#/'+section+'?project=p1');await page.getByRole('main').getByRole('heading',{level:1}).waitFor();await noOverflow();
    }
    await page.goto(url+'/#/devices?project=p1');await page.getByRole('button',{name:'Pair device',exact:true}).click();
    await page.getByLabel('Device name').fill('Test Mac');await page.getByRole('button',{name:'Generate code'}).click();await page.getByText('0123456789abcdef',{exact:true}).waitFor();
    await page.getByRole('button',{name:'Done',exact:true}).click();await noOverflow();
    assert.deepEqual(errors,[]);
   }finally{await c.close()}
  });
 }
});
