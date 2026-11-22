#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Compare packaged dashboard reads on the same disposable PostgreSQL workspace."""
import argparse
import datetime
import http.client
import json
import os
import secrets
import statistics
import time
from pathlib import Path

import smoke


def seed(env, db, n):
    sql=f"""
    INSERT INTO projects(id,workspace_id,name,repository) SELECT 'p'||g,'personal','Project '||g,'fixture/repo-'||g FROM generate_series(1,{n}) g;
    INSERT INTO manager_messages(id,workspace_id,project_id,body,request_id) SELECT 'm'||g,'personal','p'||g,repeat('message ',100),'request'||g FROM generate_series(1,{n}) g;
    INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences) SELECT 'i'||g,'personal','p'||g,'manual','grp'||g, 'Observed','Expected','Steps',now(),now(),1 FROM generate_series(1,{n}) g;
    INSERT INTO plan_versions(id,workspace_id,project_id,version,document,execution_order,approved_at) SELECT 'plan'||g,'personal','p'||g,1,jsonb_build_object('title','Plan','milestone','Milestone','assumptions','Ready','tickets',(SELECT jsonb_agg(jsonb_build_object('title','Ticket','number',t,'criteria',jsonb_build_array(repeat('criterion ',{80 if n < 100 else 98})),'verification','Run check','priority',1,'estimateHours',1,'resources',null,'dependencies',null)) FROM generate_series(1,{4 if n<100 else 36}) t)),(SELECT jsonb_agg(t ORDER BY t) FROM generate_series(1,{4 if n<100 else 36}) t),now() FROM generate_series(1,{n}) g;
    INSERT INTO pull_observations(workspace_id,project_id,number,issue_number,version,snapshot,policy_version,plan_version) SELECT 'personal','p'||g,2,1,1,'{{"title":"Fix","state":"OPEN","issueState":"OPEN","head":"head","base":"base","target":"main","criteria":"criteria","checks":"SUCCESS","checkCount":1,"review":"APPROVED","draft":false}}',1,1 FROM generate_series(1,{n}) g;
    INSERT INTO events(workspace_id,project_id,kind,subject_id) SELECT 'personal','p'||g,'project.created','p'||g FROM generate_series(1,{n}) g;
    ANALYZE;
    """
    smoke.run('psql','-X','-v','ON_ERROR_STOP=1','-d',db,'-c',sql,env=env)


def normalized(value):
    if isinstance(value, list):
        return [normalized(row) for row in value]
    if isinstance(value, dict):
        return {k: normalized(v) for k,v in value.items()}
    if isinstance(value,str) and len(value)>19 and value[10]=="T":
        try:
            return datetime.datetime.fromisoformat(value.replace("Z","+00:00"))
        except ValueError:
            pass
    return value


def measure(releases, n):
    db = "bugsnitch_dashboard_" + secrets.token_hex(8)
    host = os.environ.get("PGHOST", "")
    if not (host.startswith("/") or host in ("localhost", "127.0.0.1", "::1")) or "," in host:
        raise ValueError("Set PGHOST to a disposable local PostgreSQL socket or loopback host")
    env = {k: v for k,v in os.environ.items() if not k.startswith(("PG","BUGSNITCH_","MCP_OAUTH_","GITHUB_","SENTRY_"))}
    for k in ("PGHOST","PGPORT","PGUSER","PGPASSWORD"):
        if k in os.environ:
            env[k]=os.environ[k]
    env.update(DATABASE_URL=f"postgresql:///{db}?sslmode=disable",BUGSNITCH_OWNER_TOKEN=secrets.token_hex(32),
               BUGSNITCH_MCP_TOKEN=secrets.token_hex(32))
    results = {}
    smoke.run("createdb","-w",db,env=env)
    try:
        for index, release in enumerate(releases):
            env["BUGSNITCH_LISTEN"]=f"127.0.0.1:{smoke.port()}"
            env["BUGSNITCH_ORIGIN"]="http://"+env["BUGSNITCH_LISTEN"]
            with smoke.server(env,release.resolve()/"bugsnitch-server"):
                if index==0:
                    seed(env,db,n)
                con=http.client.HTTPConnection(env["BUGSNITCH_LISTEN"],timeout=20)
                try:
                    con.request("POST","/api/v1/login",json.dumps({"token":env["BUGSNITCH_OWNER_TOKEN"]}),
                                {"Content-Type":"application/json","Origin":env["BUGSNITCH_ORIGIN"]})
                    login=con.getresponse()
                    assert login.status==200
                    cookie=login.getheader("Set-Cookie").split(";")[0]
                    login.read()
                    for endpoint in ("state","diagnostics"):
                        samples=[]
                        for i in range(36):
                            start=time.perf_counter()
                            con.request("GET","/api/v1/"+endpoint,headers={"Cookie":cookie})
                            r=con.getresponse()
                            b=r.read()
                            assert r.status==200,(endpoint,r.status)
                            if i>=5:
                                samples.append((time.perf_counter()-start)*1000)
                        obj=json.loads(b)
                        if endpoint=="state":
                            assert len(obj["projects"])==n and len(obj["plans"])==n
                            assert n<100 or len(b)>4*1024*1024
                        obj.pop("observedAt" if endpoint=="diagnostics" else "updatedAt")
                        result=normalized(obj)
                        if index==0:
                            results[endpoint]=result
                        else:
                            assert results[endpoint]==result, f"{endpoint} disagrees with baseline"
                        p95=statistics.quantiles(samples,n=20,method="inclusive")[18]
                        print(f"{release.name} {n} projects {endpoint} ({len(b)} bytes, 31 warm reads): "
                              f"median {statistics.median(samples):.2f} ms, p95 {p95:.2f} ms",flush=True)
                finally:
                    con.close()
    finally:
        smoke.run("dropdb","-w",db,env=env)


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("release",nargs="+",type=Path,help="Directory containing matching packaged server executables, oldest first")
    parser.add_argument("--projects",nargs="+",type=int,default=[3,60,120],choices=range(1,151),metavar="N")
    args=parser.parse_args()
    if not os.getenv("PGHOST"):
        parser.error("Set PGHOST, PGPORT and PGUSER for a disposable PostgreSQL instance with CREATEDB.")
    for size in args.projects:
        measure(args.release,size)
