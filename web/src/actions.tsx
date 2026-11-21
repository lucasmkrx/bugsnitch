// SPDX-License-Identifier: MPL-2.0
import {useState,useRef,type FormEvent,type ReactNode} from 'react';
import {post,enc,messageOf,type State,type Project,type PlanDoc,type PlanTicket,type Decision,HttpError} from './api';
import {Modal,Field,link,when} from './ui';
import {refresh} from './store';

export type ModalKind='project'|'pair'|'report'|'plan';
export type Run=(work:()=>Promise<unknown>,done?:string)=>Promise<boolean>;
export function Editor({kind,state,project,close,run,busy}:{kind:ModalKind;state:State;project?:Project;close:()=>void;run:Run;busy:boolean}){
 const requestId=useRef(crypto.randomUUID());
 const [error,setError]=useState(''),[pair,setPair]=useState<{code:string;expiresAt:string}|null>(null);
 const [tickets,setTickets]=useState([0]);
 function done(){if(!busy)close()}
 async function submit(e:FormEvent<HTMLFormElement>){
  e.preventDefault();if(busy)return;
  const f=new FormData(e.currentTarget),field=(n:string)=>String(f.get(n)||'').trim(),projectId=field('projectId')||project?.id;
  let destination='',paired=false;setError('');
  const ok=await run(async()=>{
   try{
    if(kind==='project'){
     const res=await post<{id:string}>('projects',{name:field('name'),repository:field('repository')});
     destination=link('settings',res.id);
    } else if(kind==='pair'){
     if(!f.getAll('assignment').length)throw new Error('Select at least one permitted project.');
     const result=await post<{code:string;expiresAt:string}>('pairings',{name:field('name'),projectIds:f.getAll('assignment')});
     paired=true;setPair(result);
    } else if(kind==='report'){
     const res=await post<{id:string}>('incidents',{projectId,observed:field('observed'),expected:field('expected'),steps:field('steps'),requestId:requestId.current});
     destination=link('incidents',projectId,res.id);
    } else{
     const projectRecord=state.projects.find(p=>p.id===projectId);
     if(!projectRecord)throw new Error('Choose a project for this plan.');
     const items:PlanTicket[]=tickets.map((key)=>{
      const get=(n:string)=>field(key+'-'+n);
      return {number:Number(get('number')),title:get('title'),criteria:get('criteria').split('\n').map(s=>s.trim()).filter(Boolean),verification:get('verification'),priority:Number(get('priority')),estimateHours:Number(get('hours')),dependencies:get('dependencies').split(/[ ,]+/).filter(Boolean).map(Number),resources:get('resources').split(/[ ,]+/).filter(Boolean)};
     });
     const plan:PlanDoc={title:field('title'),milestone:field('milestone'),assumptions:field('assumptions'),target:field('target'),forecast:field('forecast'),tickets:items};
     await post('plans',{projectId,plan,version:projectRecord.version});
     destination=link('plans',projectId);
    }
   }catch(err){setError(messageOf(err));throw err}
  });
  if(ok&&!paired){close();if(destination)window.location.hash=destination}
 }
 const chooser=<Field title="Project"><select name="projectId" required defaultValue={project?.id||''}><option value="" disabled>Choose project</option>{state.projects.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>;
 const names={project:'Register a project',pair:'Pair a device',report:'Report an incident',plan:'Draft a plan'};
 let fields:ReactNode;
 if(kind==='project')fields=<><Field title="Project name"><input name="name" maxLength={100} required autoFocus placeholder="Customer portal"/></Field><Field title="GitHub repository"><input name="repository" required placeholder="owner/repository" pattern="[^ /]+/[^ /]+"/></Field><p className="hint">Register the repository here, then configure its GitHub App installation on the server.</p></>;
 else if(kind==='pair')fields=<><Field title="Device name"><input name="name" required maxLength={80} placeholder="Development Mac" autoFocus/></Field><fieldset><legend>Permitted projects</legend>{state.projects.map(p=><label className="checkrow" key={p.id}><input type="checkbox" defaultChecked={project?.id===p.id} name="assignment" value={p.id}/>{p.name}</label>)}</fieldset></>;
 else if(kind==='report')fields=<>{chooser}<Field title="Observed behavior"><textarea name="observed" required maxLength={2000} autoFocus/></Field><Field title="Expected behavior"><textarea name="expected" required maxLength={2000}/></Field><Field title="Reproduction steps"><textarea name="steps" maxLength={2000}/></Field><p className="hint">Describe what happened without including credentials or personal information.</p></>;
 else fields=<>{chooser}<Field title="Deliverable"><input name="title" maxLength={160} required/></Field><Field title="Milestone"><input name="milestone" required maxLength={240}/></Field><Field title="Assumptions and uncertainty"><textarea name="assumptions" maxLength={2000}/></Field><div className="form-pair"><Field title="Target date"><input type="date" name="target"/></Field><Field title="Current forecast"><input type="date" name="forecast"/></Field></div>{tickets.map((k,i)=><fieldset key={k}><legend>Issue {i+1}</legend><Field title="GitHub issue number"><input name={k+'-number'} type="number" min={1} required/></Field><Field title="Ticket title"><input name={k+'-title'} required maxLength={200}/></Field><Field title="Acceptance criteria (one per line)"><textarea name={k+'-criteria'} required/></Field><Field title="Verification method"><input name={k+'-verification'} required/></Field><div className="form-pair"><Field title="Estimated hours"><input name={k+'-hours'} type="number" min={1} max={16} defaultValue={2} required/></Field><Field title="Priority (0 is first)"><input name={k+'-priority'} type="number" min={0} max={4} defaultValue={2} required/></Field></div><Field title="Dependency issue numbers"><input name={k+'-dependencies'} placeholder="Separate with commas"/></Field><Field title="Shared resources"><input name={k+'-resources'} placeholder="Separate with commas"/></Field>{tickets.length>1&&<button type="button" onClick={()=>setTickets(tickets.filter(t=>t!==k))}>Remove ticket</button>}</fieldset>)}<button type="button" disabled={tickets.length>=64} onClick={()=>setTickets([...tickets,Math.max(...tickets)+1])}>Add another ticket</button></>;
 return <Modal title={names[kind]} close={done}>{pair?<><p>Use this one-time code with the pairing command on your computer.</p><code className="pair-code">{pair.code}</code><pre>bugsnitch pair --server {window.location.origin} --code {pair.code}</pre><p className="hint">Expires {when(pair.expiresAt)}. The device appears after enrollment succeeds.</p><button className="primary" onClick={done}>Done</button></>:<form onSubmit={submit}>{fields}{error&&<p className="alert" role="alert">{error}</p>}<div className="form-footer"><button type="button" onClick={done} disabled={busy}>Cancel</button><button className="primary" disabled={busy}>{busy?'Submitting…':{project:'Register project',pair:'Generate code',report:'Submit report',plan:'Save draft'}[kind]}</button></div></form>}</Modal>
}

type AnswerIntent={id:string;answer:string;version:number;subjectVersion:number;requestId:string};
export async function decide(workspace:string,d:Decision,answer:string){
 const key='bugsnitch:answer:'+workspace+':'+d.id;
 const previous:AnswerIntent|null=JSON.parse(sessionStorage.getItem(key)||'null');
 const current=(await refresh()).decisions.find(item=>item.id===d.id);
 if(previous&&current?.status==='answered'&&current.requestId===previous.requestId&&current.answer===previous.answer){
  sessionStorage.removeItem(key);
  if(answer!==previous.answer)throw new Error('Your earlier answer was recorded. Review the current decision.');
  return;
 }
 if(!current||current.status!=='pending'||d.version!==current.version||d.subjectVersion!==current.subjectVersion||!current.options.includes(answer)){
  throw new Error('This decision changed. Review its current state before answering.');
 }
 if(previous&&(previous.answer!==answer||previous.version!==d.version||previous.subjectVersion!==d.subjectVersion))throw new Error('An earlier answer is awaiting confirmation. Retry the original choice or refresh its status.');
 const intent=previous||{id:d.id,answer,version:d.version,subjectVersion:d.subjectVersion,requestId:crypto.randomUUID()};
 sessionStorage.setItem(key,JSON.stringify(intent));
 try{
  await post('decisions/'+enc(d.id)+'/answer',{answer,version:d.version,requestId:intent.requestId});
  sessionStorage.removeItem(key);
 }catch(error){
  if(error instanceof HttpError&&[400,403,404,409].includes(error.status))sessionStorage.removeItem(key);
  throw error;
 }
}
