// SPDX-License-Identifier: MPL-2.0
import {useState,type FormEvent} from 'react';
import {type Incident,type State,type Project,post,request,enc,messageOf} from './api';
import type {Run} from './actions';
import {Field,Panel,Badge,Empty,when} from './ui';

export function Pulls({state,project,locked,run}:{state:State;project?:Project;locked:boolean;run:Run}){
 const pulls=state.pullRequests.filter(p=>!project||p.projectId===project.id);
 const repository=(id:string)=>state.projects.find(p=>p.id===id)?.repository;
 async function inspect(e:FormEvent<HTMLFormElement>){
  e.preventDefault();const form=new FormData(e.currentTarget),id=String(form.get('project')),number=Number(form.get('number')),issueNumber=Number(form.get('issue'));
  const version=pulls.find(p=>p.projectId===id&&p.number===number)?.version||0;
  await run(()=>post('projects/'+enc(id)+'/pulls/'+number+'/refresh',{issueNumber,version}),'Inspection refreshed from GitHub.');
 }
 return <><Panel title="Inspect a pull request"><form className="panel-body" onSubmit={inspect}><Field title="Repository"><select name="project" required defaultValue={project?.id||''}><option disabled value="">Choose project</option>{(project?[project]:state.projects).map(p=><option key={p.id} value={p.id}>{p.repository}</option>)}</select></Field><div className="form-pair"><Field title="Pull request number"><input type="number" min={1} name="number" required/></Field><Field title="Canonical issue number"><input type="number" min={1} name="issue" required/></Field></div><button disabled={locked||!state.projects.length}>Inspect pull request</button></form></Panel>{pulls.length?pulls.map(p=><Panel key={p.projectId+p.number} title={'#'+p.number+' '+p.title} action={<Badge>{p.state}</Badge>}><div className="panel-body"><a href={'https://github.com/'+repository(p.projectId)+'/pull/'+p.number}>Open on GitHub</a><p>{repository(p.projectId)} · Issue #{p.issueNumber} · Inspected {when(p.observedAt)}</p><dl className="detail"><div><dt>Target branch</dt><dd>{p.target}</dd></div><div><dt>Head checks</dt><dd>{p.checks||'Unknown'} · {p.checkCount} checks</dd></div><div><dt>GitHub review</dt><dd>{p.review||'Unknown'}</dd></div><div><dt>Head / base</dt><dd><code>{p.head}<br/>{p.base||'Unknown'}</code></dd></div><div><dt>Combined validation</dt><dd>{p.validation||'Missing'}</dd></div><div><dt>Independent review</dt><dd>{p.independentReview||'Missing'}</dd></div></dl><h3>Merge blockers</h3><ul>{p.blockers.map(b=><li key={b}>{b}</li>)}</ul><button disabled={locked} onClick={()=>void run(()=>post('projects/'+enc(p.projectId)+'/pulls/'+p.number+'/refresh',{issueNumber:p.issueNumber,version:p.version}),'Inspection refreshed.')}>Refresh inspection</button></div></Panel>):<Panel title="Integration queue"><Empty title="No inspected pull requests" icon="prs">Inspect a pull request to load its actual checks, review, and merge blockers.</Empty></Panel>}</>
}

interface Receipt{candidate:string;build:string;at:string;evidence:string}
interface Verdict{status:string;reason:string;environment:string;receiptSource:string;fix?:Receipt;deployment?:Receipt;validation?:Receipt}
export function Verification({incident,locked}:{incident:Incident;locked:boolean}){
 const [verdict,setVerdict]=useState<Verdict>(),[error,setError]=useState(''),[loading,setLoading]=useState(false);
 return <Panel title="Environment verification"><form className="panel-body" onSubmit={async e=>{e.preventDefault();const env=String(new FormData(e.currentTarget).get('env'));setError('');setVerdict(undefined);setLoading(true);try{setVerdict(await request<Verdict>('projects/'+enc(incident.projectId)+'/incidents/'+enc(incident.id)+'/verification?environment='+enc(env)))}catch(err){setError(messageOf(err))}finally{setLoading(false)}}}><Field title="Deployment environment"><input name="env" defaultValue={incident.environment||''} placeholder="production" required maxLength={80} onChange={()=>setVerdict(undefined)}/></Field><button disabled={locked||loading}>{loading?'Checking…':'Check verification'}</button>{error&&<p className="alert" role="alert">{error}</p>}</form>{verdict&&<div className="panel-body"><Badge>{verdict.status}</Badge><p>{verdict.environment} · {verdict.reason}</p>{([['Fix',verdict.fix],['Deployment',verdict.deployment],['Validation',verdict.validation]] as const).map(([k,r])=>r&&<details key={k}><summary>{k} receipt · {when(r.at)}</summary><p>{r.evidence}</p><p>Revision <code>{r.candidate}</code></p>{r.build&&<p>Build {r.build}</p>}<p>Recorded by {verdict.receiptSource}</p></details>)}</div>}</Panel>
}
