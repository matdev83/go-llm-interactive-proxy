#!/usr/bin/env python3
"""Run one packet's focused Go tests and reject vacuous success. Not a release certificate."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile


def inspect_events(lines, prefix: str, exit_code: int) -> dict:
    ran=set();passed=set();skipped=set();failed=set();package_failed=set()
    malformed=0
    for line in lines:
        if not line.strip():continue
        if len(line)>8*1024*1024:
            malformed+=1;continue
        try:ev=json.loads(line)
        except (ValueError,TypeError):
            # Go tool setup diagnostics can precede JSON; a nonzero exit still fails.
            continue
        if not isinstance(ev,dict):continue
        action=ev.get('Action');test=ev.get('Test','');package=ev.get('Package','')
        if action=='fail' and not test:package_failed.add(package)
        if not test.startswith(prefix):continue
        name=package+'::'+test
        if action=='run':ran.add(name)
        elif action=='pass':passed.add(name)
        elif action=='skip':skipped.add(name)
        elif action=='fail':failed.add(name)
    reasons=[]
    if exit_code!=0:reasons.append(f'go test exit code {exit_code}')
    if not ran:reasons.append('zero matching tests executed')
    if skipped:reasons.append('matching test or subtest skipped')
    if failed or package_failed:reasons.append('test/package failure')
    if not ran<=passed:reasons.append('not every matching executed case passed')
    if malformed:reasons.append('oversized unparseable test event')
    return {'status':'TEST_GATE_FAIL' if reasons else 'TEST_GATE_PASS','tests_run':sorted(ran),
            'tests_passed':sorted(passed),'tests_skipped':sorted(skipped),'tests_failed':sorted(failed),
            'package_failures':sorted(package_failed),'reasons':reasons}


def self_test() -> int:
    pref='TestFinancialSafety_T01_'
    def event(action,test='TestFinancialSafety_T01_Literal'):
        return json.dumps({'Action':action,'Test':test,'Package':'fixture'})+'\n'
    cases=[('pass',[event('run'),event('pass')],0,True),
           ('no-match',[event('run','TestOther'),event('pass','TestOther')],0,False),
           ('skip',[event('run'),event('skip')],0,False),
           ('failed',[event('run'),event('fail')],1,False),
           ('unfinished',[event('run')],0,False),
           ('failed-package',[event('run'),event('pass'),event('fail','')],0,False)]
    for name,lines,code,want in cases:
        got=inspect_events(lines,pref,code)['status']=='TEST_GATE_PASS'
        if got!=want:
            print(f'SELF_TEST_FAIL: {name}');return 1
    print('SELF_TEST_PASS: 6 synthetic event-validation cases; no proxy implementation executed')
    return 0


def main() -> int:
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--task');p.add_argument('--repo',type=Path,default=Path.cwd())
    p.add_argument('--output',type=Path);p.add_argument('--timeout',type=int,default=1800)
    p.add_argument('--self-test',action='store_true')
    a=p.parse_args()
    if a.self_test:return self_test()
    if not a.task:p.error('--task is required except with --self-test')
    if a.timeout<=0:p.error('--timeout must be positive')
    spec=Path(__file__).resolve().parents[1]
    try:
        manifest=json.loads((spec/'execution/task-manifest.json').read_text(encoding='utf-8'))
        task=next(t for t in manifest['tasks'] if t['id']==a.task or t['packet_id']==a.task)
    except (OSError,ValueError,StopIteration) as exc:
        print(f'Cannot load task: {exc}',file=sys.stderr);return 2
    repo=a.repo.resolve()
    if not (repo/'go.mod').is_file():
        print('Run from the repository root, or provide --repo; no go.mod found.',file=sys.stderr);return 2
    try:
        head=subprocess.check_output(['git','-C',str(repo),'rev-parse','HEAD'],text=True).strip()
    except (OSError,subprocess.CalledProcessError):
        print('Cannot establish actual code SHA.',file=sys.stderr);return 2
    workdir=(repo/task.get('working_directory','.')).resolve()
    if not workdir.is_relative_to(repo) or not (workdir/'go.mod').is_file():
        print('Task module working directory is invalid or has no go.mod.',file=sys.stderr);return 2
    missing=[name for name in task.get('required_env',[]) if not os.environ.get(name)]
    if missing:
        print('Mandatory topology configuration missing: '+', '.join(missing),file=sys.stderr);return 2
    out=(a.output or Path(tempfile.mkdtemp(prefix='financial-task-gate-'))).resolve()
    out.mkdir(parents=True,exist_ok=True)
    logfile=out/(task['packet_id']+'.go-test.jsonl')
    timed_out=False
    command=task['command']
    try:
        with logfile.open('w',encoding='utf-8') as log:
            proc=subprocess.Popen(command,cwd=workdir,stdout=log,stderr=subprocess.STDOUT,
                                  start_new_session=(os.name!='nt'))
            try:code=proc.wait(timeout=a.timeout)
            except subprocess.TimeoutExpired:
                timed_out=True
                if os.name=='nt':
                    subprocess.run(['taskkill','/PID',str(proc.pid),'/T','/F'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=False)
                else:os.killpg(proc.pid,signal.SIGKILL)
                code=proc.wait()
        with logfile.open(encoding='utf-8',errors='replace') as lines:
            result=inspect_events(lines,task['test_prefix'],code)
        if timed_out:
            result['status']='TEST_GATE_FAIL';result['reasons'].append('test timeout')
        digest=hashlib.sha256()
        with logfile.open('rb') as raw:
            for chunk in iter(lambda:raw.read(1024*1024),b''):digest.update(chunk)
        result.update({'task_id':task['id'],'packet_id':task['packet_id'],'head_sha':head,
                       'argv':command,'working_directory':str(workdir),'exit_code':code,'log_sha256':digest.hexdigest(),
                       'log_path':str(logfile),'task_completion_certified':False,
                       'release_certified':False})
        receipt=out/(task['packet_id']+'.test-gate.json')
        receipt.write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
        print(json.dumps({'status':result['status'],'task':task['id'],
                          'matching_tests':len(result['tests_run']),'reasons':result['reasons'],
                          'receipt':str(receipt),'release_certified':False},indent=2))
        return 0 if result['status']=='TEST_GATE_PASS' else 1
    except OSError as exc:
        print(f'Test command could not execute: {exc}',file=sys.stderr);return 2
if __name__=='__main__':sys.exit(main())
