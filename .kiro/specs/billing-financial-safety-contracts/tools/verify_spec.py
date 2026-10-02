#!/usr/bin/env python3
"""Validate this specification archive, not the implementation of the proxy."""
from __future__ import annotations
import argparse
import hashlib
import json
import re
import sys
import shutil
import tempfile
import copy
sys.dont_write_bytecode = True
from pathlib import Path


def validate(spec: Path, check_hashes: bool = True) -> dict:
    errors: list[str] = []
    def require(condition: bool, message: str) -> None:
        if not condition:
            errors.append(message)
    def load(rel: str):
        return json.loads((spec / rel).read_text(encoding='utf-8'))
    required = ['spec.json', 'requirements.md', 'research.md', 'design.md', 'tasks.md',
                'START-HERE.md','acceptance.md','execution/task-manifest.json',
                'execution/traceability.json','execution/acceptance.json',
                'execution/interface-contracts.md','execution/gates.md','execution/order.md',
                'reference/source-review.md','tools/run_task_gate.py','coverage/universe.json',
                'coverage/matrix-policy.json','coverage/economic-fixtures.json','coverage/contracts.md',
                'coverage/round2-gaps.json','tools/coverage_lattice.py','tools/verify_coverage.py']
    for p in required:
        require((spec/p).is_file(), f'missing required artifact: {p}')
    if errors:
        return {'status':'FAIL','errors':errors}
    meta=load('spec.json'); manifest=load('execution/task-manifest.json')
    trace=load('execution/traceability.json'); matrix=load('execution/acceptance.json')
    require(meta['phase']=='tasks-generated','wrong delivery lifecycle phase')
    require(meta['ready_for_implementation'] is True,'spec not marked ready')
    require(meta['implementation_complete'] is False and meta['completed'] is False,
            'spec archive must not claim implementation complete')
    for p in ['requirements','design','tasks']:
        require(meta['approvals'][p]=={'generated':True,'approved':True},f'unapproved spec phase: {p}')
    tasks={t['id']:t for t in manifest['tasks']}; scenarios={s['id']:s for s in matrix['scenarios']}
    require(meta.get('spec_revision')==2 and manifest.get('revision')==2,'wrong specification revision')
    require(manifest['task_count']==len(tasks) and manifest['requirement_count']==len(trace['requirements']) and manifest['scenario_count']==len(scenarios),'manifest counts disagree')
    require(meta['scope_counts']['implementation_tasks']==113 and meta['scope_counts']['acceptance_criteria']==147 and meta['scope_counts']['acceptance_scenarios']==112,'metadata counts disagree')
    require(manifest['parallel_groups']=={},'revision 2 does not authorize parallel groups')
    require(len(tasks)==len(manifest['tasks'])==113,'duplicate/missing task IDs')
    require(len(scenarios)==len(matrix['scenarios'])==112,'duplicate/missing scenario IDs')
    require(len(trace['requirements'])==147,'criterion count is not147')
    require(set(trace['contracts'])=={f'C{i:02}' for i in range(1,12)},'missing original contracts')
    require(set(trace['findings'])=={f'F{i:02}' for i in range(1,15)},'missing review findings')
    reqtext=(spec/'requirements.md').read_text(encoding='utf-8')
    parsed={}; group=None
    for line in reqtext.splitlines():
        m=re.match(r'### Requirement (\d+):',line)
        if m: group=m.group(1)
        m=re.match(r'^(\d+)\. (.+)$',line)
        if m and group:
            parsed[group+'.'+m.group(1)]=m.group(2)
    require(set(parsed)==set(trace['requirements']),'canonical requirement IDs differ from traceability')
    for rid,row in trace['requirements'].items():
        require(parsed.get(rid)==row['text'],f'criterion body mismatch: {rid}')
        require(bool(row['tasks']) and bool(row['scenarios']) and bool(row['design']),f'unowned criterion: {rid}')
        require(all(t in tasks for t in row['tasks']),f'unknown task in {rid}')
        require(all(s in scenarios for s in row['scenarios']),f'unknown scenario in {rid}')
        for t in row['tasks']:
            if t in tasks: require(rid in tasks[t]['requirements'],f'asymmetric task trace {rid}/{t}')
    design=(spec/'design.md').read_text(encoding='utf-8')
    designs=set(re.findall(r'^## (D\d{2}) —',design,re.M))
    require(designs=={f'D{i:02}' for i in range(1,25)},'missing design sections')
    require(len(design.splitlines())<1000,'canonical design exceeds local template scale guideline')
    design_bodies={}
    for match in re.finditer(r'^## (D\d{2}) — ([^\n]+)\n(.*?)(?=^## |\Z)',design,re.M|re.S):
        design_bodies[match[1]]=(match[2],match[3].strip())
    requirement_digest=hashlib.sha256(reqtext.encode()).hexdigest()
    interface_text=(spec/'execution/interface-contracts.md').read_text(encoding='utf-8')
    media_text=(spec/'coverage/contracts.md').read_text(encoding='utf-8')
    done=set(); order=manifest['execution_order']
    require(len(order)==len(set(order))==113,'execution order missing/duplicate tasks')
    for tid in order:
        if tid not in tasks:errors.append(f'unknown scheduled task {tid}');continue
        t=tasks[tid]
        require(set(t['depends'])<=done,f'unsatisfied/cyclic dependency before {tid}')
        done.add(tid)
    for tid,t in tasks.items():
        require(t['status']=='not_started',f'false execution status: {tid}')
        require(all(x in tasks for x in t['depends']),f'unknown dependency: {tid}')
        require(all(x in parsed for x in t['requirements']),f'unknown criterion: {tid}')
        require(all(x in scenarios for x in t['scenarios']),f'unknown scenario: {tid}')
        require(all(x in designs for x in t['design']),f'unknown design: {tid}')
        require(all(x in trace['findings'] for x in t['findings']),f'unknown finding: {tid}')
        for key in ['goal','sources','outputs','steps','acceptance','command']:
            require(bool(t[key]),f'missing {key}: {tid}')
        require(0<t['max_changed_go_files']<=16,f'oversized change scope: {tid}')
        b=t['context_budget']
        require(b['target_tokens']<b['checkpoint_tokens']<b['hard_stop_tokens']<1000000,
                f'invalid context budget: {tid}')
        require(b['compaction_allowed'] is False,f'compaction enabled: {tid}')
        packet=spec/t['packet_path']
        require(packet.is_file(),f'missing packet: {tid}')
        if packet.is_file():
            body=packet.read_text(encoding='utf-8')
            require(len(body.encode())<160000,f'oversized packet artifact: {tid}')
            for rid in t['requirements']:require(f'**{rid}:** {parsed[rid]}' in body,f'packet criterion drift {tid}/{rid}')
            for sid in t['scenarios']:require(f'**{sid} —' in body,f'packet scenario omitted {tid}/{sid}')
            require(t['test_prefix'] in body and t['gate_wrapper'] in body,f'packet gate missing: {tid}')
            require(hashlib.sha256(packet.read_bytes()).hexdigest()==t.get('packet_content_sha256'),f'packet content digest drift: {tid}')
            require(t.get('canonical_requirements_sha256')==requirement_digest,f'packet requirements digest drift: {tid}')
            require(t.get('packet_revision')==2,f'stale packet revision: {tid}')
            require(t.get('canonical_interface_sha256')==hashlib.sha256(interface_text.encode()).hexdigest(),f'shared interface digest drift: {tid}')
            require(interface_text in body,f'shared interface text drift: {tid}')
            require(t.get('canonical_media_contract_sha256')==hashlib.sha256(media_text.encode()).hexdigest(),f'media contract digest drift: {tid}')
            if any(int(r.split('.')[0])>=17 for r in t['requirements']):
                require(media_text in body,f'media contract text drift: {tid}')
            require(f'/ task {tid} — {t["title"]}' in body,f'packet identity/title drift: {tid}')
            for did in t['design']:
                if did not in design_bodies:continue
                title,section=design_bodies[did]
                digest=hashlib.sha256((title+'\n'+section).encode()).hexdigest()
                require(t.get('embedded_design_sha256',{}).get(did)==digest,f'canonical design hash drift: {tid}/{did}')
                require(f'### {did} — {title}\n\n'+section in body,f'embedded design text drift: {tid}/{did}')
            for sid in t['scenarios']:
                if sid in scenarios:
                    require(scenarios[sid]['setup'] in body and scenarios[sid]['expected'] in body,f'packet scenario text drift: {tid}/{sid}')
            if t.get('module'):
                require(t.get('working_directory')==t['module'],f'module gate runs in wrong directory: {tid}')
    for fid,row in trace['findings'].items():
        require(bool(row['primary_tasks']) and all(x in tasks for x in row['primary_tasks']),f'unowned finding: {fid}')
        require(bool(row['scenarios']),f'untested finding: {fid}')
    for cid,row in trace['contracts'].items():
        require(bool(row['requirements']) and bool(row['scenarios']),f'unmapped contract: {cid}')
        require(all(x in parsed for x in row['requirements']),f'contract requirement unknown: {cid}')
    for sid,s in scenarios.items():
        require(bool(s['requirements']) and bool(s['implementation_tasks']),f'unowned scenario: {sid}')
        require(s['mandatory'] is True and bool(s['topologies']),f'optional/empty scenario: {sid}')
        require(all(r in parsed for r in s['requirements']),f'unknown scenario criterion: {sid}')
    for name,members in manifest['parallel_groups'].items():
        require(members==['5.1','5.2','5.3','5.4'],f'undesignated parallel group: {name}')
        seen=set()
        for tid in members:
            roots=set(tasks[tid]['allowed_edit_roots'])
            require(not (seen&roots),f'parallel shared edit owner: {tid}')
            seen|=roots
    plan=(spec/'tasks.md').read_text(encoding='utf-8')
    require(len(re.findall(r'^- \[ \] \d+\.\d+ ',plan,re.M))==113,'wrong task checkbox count')
    require(not re.search(r'^- \[[xX]\]',plan,re.M),'implementation falsely checked complete')
    source=spec/'reference/source-review.md'
    require(hashlib.sha256(source.read_bytes()).hexdigest()==meta['source_review_sha256'], 'source review was changed')
    for path in spec.rglob('*.md'):
        if 'reference' in path.parts:continue
        text=path.read_text(encoding='utf-8')
        require(not re.search(r'\{\{[^}]+\}\}|\bTBD\b|\bTODO\b|<feature-name>',text),f'unresolved placeholder: {path.name}')
        for target in re.findall(r'\[[^\]]+\]\(([^)]+)\)',text):
            if '://' in target or target.startswith('#'):continue
            rel=target.split('#',1)[0]
            require((path.parent/rel).exists(),f'broken relative link: {path.name} -> {rel}')
    # Validate the separately specified full coverage universe and independent literal math.
    import importlib.util
    checker_path=spec/'tools/verify_coverage.py'
    module_spec=importlib.util.spec_from_file_location('spec_coverage_check',checker_path)
    module=importlib.util.module_from_spec(module_spec)
    module_spec.loader.exec_module(module)
    errors.extend('coverage: '+x for x in module.validate_values(*module.load(spec)))
    gaps=load('coverage/round2-gaps.json')['gaps']
    require(len(gaps)==12 and {g['id'] for g in gaps}=={f'R2-{i:02}' for i in range(1,13)},'missing second-round gap')
    for gap in gaps:
        require(bool(gap['tasks']) and all(x in tasks for x in gap['tasks']),f'unowned second-round gap: {gap["id"]}')
    root=spec.parents[2]
    mf=root/'MANIFEST.sha256'
    require(not check_hashes or mf.is_file(), 'MANIFEST.sha256 missing; validate the full extracted archive, or use --skip-hashes for structure-only checks')
    if check_hashes and mf.is_file():
        declared=set()
        for line in mf.read_text(encoding='utf-8').splitlines():
            digest,rel=line.split('  ',1)
            require(rel not in declared,f'duplicate hash manifest path: {rel}')
            declared.add(rel)
            dest=(root/rel).resolve()
            if not dest.is_relative_to(root.resolve()):
                errors.append(f'unsafe manifest path: {rel}');continue
            require(dest.is_file(),f'missing hashed artifact: {rel}')
            if dest.is_file():require(hashlib.sha256(dest.read_bytes()).hexdigest()==digest,f'hash mismatch: {rel}')
        actual={str(p.relative_to(root)).replace('\\','/') for p in root.rglob('*') if p.is_file() and p!=mf}
        require(actual==declared,'hash manifest inventory differs from all archive artifacts')
    return {'status':'FAIL' if errors else 'PASS','checks':'artifact structure,147 criteria,113 task DAG/packets,112 scenarios,11 contracts,14 findings,24 designs,complete multimodal obligations,scope budgets,source retention,links',
            'implementation_verified':False,'hashes_checked':check_hashes and mf.is_file(),'errors':errors}



def self_test(spec: Path) -> dict:
    """Mutate copied specification data only; no proxy code or external services run."""
    initial=validate(spec,False)
    if initial['status']!='PASS':
        return {'status':'FAIL','reason':'baseline must pass before negative controls','errors':initial['errors']}
    with tempfile.TemporaryDirectory(prefix='billing-spec-mutations-') as directory:
        target=Path(directory)/'package'
        shutil.copytree(spec,target,ignore=shutil.ignore_patterns('__pycache__','*.pyc'))
        paths=['spec.json','execution/task-manifest.json','execution/acceptance.json','execution/traceability.json',
               'requirements.md','design.md','execution/interface-contracts.md','coverage/contracts.md','execution/T01.md']
        saved={rel:(target/rel).read_bytes() for rel in paths}
        rejected=[]
        def set_json(rel, change):
            value=json.loads(saved[rel]);change(value)
            (target/rel).write_text(json.dumps(value),encoding='utf-8')
        mutations=[
          ('false-implementation-complete',lambda:set_json('spec.json',lambda x:x.update(implementation_complete=True))),
          ('missing-contract',lambda:set_json('execution/traceability.json',lambda x:x['contracts'].pop('C01'))),
          ('missing-finding',lambda:set_json('execution/traceability.json',lambda x:x['findings'].pop('F14'))),
          ('cycle',lambda:set_json('execution/task-manifest.json',lambda x:x['tasks'][0]['depends'].append(x['execution_order'][-1]))),
          ('unknown-dependency',lambda:set_json('execution/task-manifest.json',lambda x:x['tasks'][0]['depends'].append('99.99'))),
          ('compaction-enabled',lambda:set_json('execution/task-manifest.json',lambda x:x['tasks'][0]['context_budget'].update(compaction_allowed=True))),
          ('stale-packet-revision',lambda:set_json('execution/task-manifest.json',lambda x:x['tasks'][0].update(packet_revision=1))),
          ('missing-packet',lambda:(target/'execution/T01.md').unlink()),
          ('packet-content-changed',lambda:(target/'execution/T01.md').write_bytes(saved['execution/T01.md']+b'\nchanged\n')),
          ('upstream-design-changed',lambda:(target/'design.md').write_text(saved['design.md'].decode().replace('## D01 —','## D01 — changed ',1))),
          ('upstream-interface-changed',lambda:(target/'execution/interface-contracts.md').write_bytes(saved['execution/interface-contracts.md']+b'\nchanged\n')),
          ('upstream-media-changed',lambda:(target/'coverage/contracts.md').write_bytes(saved['coverage/contracts.md']+b'\nchanged\n')),
          ('scenario-made-optional',lambda:set_json('execution/acceptance.json',lambda x:x['scenarios'][-1].update(mandatory=False))),
          ('scenario-oracle-changed',lambda:set_json('execution/acceptance.json',lambda x:x['scenarios'][-1].update(expected='changed oracle'))),
          ('criterion-body-changed',lambda:(target/'requirements.md').write_text(saved['requirements.md'].decode().replace('1. Where monetary billing','1. Where changed monetary billing',1))),
        ]
        for name,mutate in mutations:
            for rel,body in saved.items():(target/rel).write_bytes(body)
            mutate()
            try:
                result=validate(target,False)
            except (OSError,ValueError,KeyError,TypeError) as exc:
                result={'status':'FAIL','errors':[str(exc)]}
            if result['status']!='FAIL':
                return {'status':'FAIL','accepted_invalid_mutation':name,'implementation_verified':False}
            rejected.append(name)
    return {'status':'PASS','kind':'SPECIFICATION_NEGATIVE_CONTROLS_ONLY','rejected_mutations':rejected,'implementation_verified':False}

def main() -> int:
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec',type=Path,default=Path(__file__).resolve().parents[1])
    parser.add_argument('--skip-hashes',action='store_true')
    parser.add_argument('--self-test',action='store_true')
    args=parser.parse_args()
    try: result=self_test(args.spec.resolve()) if args.self_test else validate(args.spec.resolve(),not args.skip_hashes)
    except (OSError,ValueError,KeyError,TypeError) as exc:
        result={'status':'FAIL','implementation_verified':False,'errors':[f'malformed package: {exc}']}
    print(json.dumps(result,indent=2))
    return 0 if result['status']=='PASS' else 1
if __name__=='__main__':sys.exit(main())
