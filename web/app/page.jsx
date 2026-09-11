'use client';
import React, { useEffect, useState } from 'react';

// Page provides account access, problem browsing, staff authoring, and submission history.
export default function Page() {
 const [token,setToken]=useState(''),[user,setUser]=useState(null),[items,setItems]=useState([]),[problem,setProblem]=useState(null),[history,setHistory]=useState([]),[message,setMessage]=useState(''),[source,setSource]=useState(''),[language,setLanguage]=useState('python'),[draft,setDraft]=useState('');
 // request uses same-origin proxying and keeps bearer credentials out of persistent browser storage.
 async function request(path,method='GET',body,session=token) {
  const response=await fetch('/v1'+path,{method,headers:{'Content-Type':'application/json',...(session?{Authorization:'Bearer '+session}:{})},...(body?{body:JSON.stringify(body)}:{})});
  if(!response.ok) throw new Error(`Request failed (${response.status})`);
  return response.status===204?null:response.json();
 }
 async function loadProblems(){try{setItems((await request('/problems')).problems);}catch(e){setMessage(e.message);}}
 useEffect(()=>{loadProblems();},[]);
 async function authenticate(event){event.preventDefault();try{const data=new FormData(event.currentTarget);const body={email:data.get('email'),password:data.get('password')};if(event.nativeEvent.submitter.value==='register')await request('/auth/register','POST',{...body,username:data.get('username')});const session=(await request('/auth/login','POST',body)).token;setToken(session);setUser(await request('/users/me','GET',undefined,session));setHistory((await request('/users/me/submissions','GET',undefined,session)).submissions);setMessage('');}catch(e){setMessage(e.message);}}
 async function select(p){try{const full=p.statement?p:await request('/problems/'+p.id);setProblem(full);setLanguage(full.languages[0]);}catch(e){setMessage(e.message);}}
 async function submit(event){event.preventDefault();try{const result=await request('/submissions','POST',{problem_id:problem.id,language_id:language,source_code:source});setMessage('Submission '+result.submission_id+' queued');setHistory((await request('/users/me/submissions')).submissions);}catch(e){setMessage(e.message);}}
 async function refresh(){try{setHistory((await request('/users/me/submissions')).submissions);}catch(e){setMessage(e.message);}}
 async function save(event){event.preventDefault();try{const data=JSON.parse(draft);await request('/admin/problems'+(data.id?'/'+data.id:''),data.id?'PUT':'POST',data);setMessage('Problem saved');await loadProblems();}catch(e){setMessage(e.message);}}
 return <main><header><p className="eyebrow">ONLINE CODING JUDGE</p><h1>Practice. Submit. Improve.</h1><p>Explore programming problems and track your solutions.</p></header>
 {message&&<p role="status">{message}</p>}
 {!user?<section><h2>Your account</h2><form onSubmit={authenticate}><label>Username (registration)<input name="username" autoComplete="username"/></label><label>Email<input name="email" type="email" required autoComplete="email"/></label><label>Password<input name="password" type="password" required autoComplete="current-password"/></label><button value="login">Log in</button> <button value="register">Register</button></form></section>:<section><p>Signed in as {user.username}</p><button onClick={async()=>{try{await request('/auth/logout','POST');setToken('');setUser(null);setHistory([]);}catch(e){setMessage(e.message);}}}>Log out</button></section>}
 <div className="columns"><section><h2>Problem catalog</h2>{items.length===0&&<p>No published problems yet.</p>}{items.map(p=><p key={p.id}><button onClick={()=>select(p)}>{p.title}</button> <small>{p.difficulty}</small></p>)}</section>
 <section>{problem?<><h2>{problem.title}</h2><p className="statement">{problem.statement}</p><p>{problem.time_limit_ms} ms · {problem.memory_limit_mb} MB</p>{problem.tests?.map((sample,i)=><div key={i}><h3>Sample {i+1}</h3><pre>{sample.input}</pre><pre>{sample.expected}</pre></div>)}{user&&<form onSubmit={submit}><label>Language<select value={language} onChange={e=>setLanguage(e.target.value)}>{problem.languages.map(l=><option key={l}>{l}</option>)}</select></label><label>Source code<textarea value={source} onChange={e=>setSource(e.target.value)} required maxLength={65536} rows={12}/></label><button>Submit solution</button></form>}</>:<p>Select a problem to get started.</p>}</section></div>
 {user&&<section><h2>Your submissions</h2><button onClick={refresh}>Refresh status</button><table><thead><tr><th>Submission</th><th>Language</th><th>Status</th><th>Verdict</th><th>Runtime</th></tr></thead><tbody>{history.map(s=><tr key={s.submission_id}><td>{s.submission_id}</td><td>{s.language_id}</td><td>{s.status}</td><td>{s.verdict||'Pending'}</td><td>{s.runtime_ms} ms</td></tr>)}</tbody></table></section>}
 {user?.role==='admin'&&<section><h2>Problem authoring</h2><p>Create or update a problem using its JSON definition. Include an id to update an existing problem.</p><form onSubmit={save}><label>Problem definition<textarea rows={14} value={draft} onChange={e=>setDraft(e.target.value)} placeholder={'{"slug":"sum","title":"Sum","statement":"Add two integers","difficulty":"easy","time_limit_ms":1000,"memory_limit_mb":128,"status":"published","languages":["python","cpp"],"tests":[{"input":"1 2","expected":"3","hidden":false}]}'}/></label><button>Save problem</button></form></section>}
 </main>;
}
