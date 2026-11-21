// SPDX-License-Identifier: MPL-2.0
import {useSyncExternalStore} from 'react';
import {HttpError,messageOf,readState,type State} from './api';
type Snapshot={state?:State; signedOut:boolean; online:boolean; error:string; loading:boolean};
let current:Snapshot={signedOut:false,online:navigator.onLine,error:'',loading:true};
let sequence=0;
const listeners=new Set<()=>void>();
function update(delta:Partial<Snapshot>){current={...current,...delta};listeners.forEach(fn=>fn())}
export function forget(){sequence++;update({state:undefined,signedOut:true,error:'',loading:false})}
export async function refresh():Promise<State>{
 const seq=++sequence;
 try{
  const data=await readState();
  if(seq===sequence)update({state:data,signedOut:false,loading:false,online:navigator.onLine,error:''});
  return data;
 } catch(e){
  if(seq===sequence){
   if(e instanceof HttpError&&e.status===401)forget();
   else update({error:messageOf(e),loading:false,online:navigator.onLine})
  }
  throw e;
 }
}
const reload=()=>{void refresh().catch(()=>{})};
export function startPolling(){
 const online=()=>{update({online:true,error:'Refreshing workspace…'});reload()};
 const offline=()=>{sequence++;update({online:false,error:'Offline. Displaying the last received workspace state.'})};
 const visible=()=>{if(document.visibilityState==='visible'&&navigator.onLine)reload()};
 window.addEventListener('online',online);window.addEventListener('offline',offline);document.addEventListener('visibilitychange',visible);
 const timer=setInterval(()=>{if(!current.signedOut)visible()},12000);reload();
 return ()=>{clearInterval(timer);window.removeEventListener('online',online);window.removeEventListener('offline',offline);document.removeEventListener('visibilitychange',visible)}
}
function subscribe(fn:()=>void){listeners.add(fn);return()=>{listeners.delete(fn)}}
export const useWorkspace=()=>useSyncExternalStore(subscribe,()=>current);
