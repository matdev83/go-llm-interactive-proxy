#!/usr/bin/env python3
"""Check the revision-3 coverage specification and synthetic arithmetic, not live billing."""
from __future__ import annotations
import argparse
import copy
from fractions import Fraction
import importlib.util
import json
from pathlib import Path
import sys

EXPECTED_FRONTENDS = {'openairesponses','openailegacy','anthropic','gemini','openresponses'}
EXPECTED_BUILTINS = {'openairesponses','openailegacy','anthropic','alibabatokenplanintl','gemini','bedrock',
                     'custom-openai-responses-compatible','custom-openai-legacy-compatible',
                     'custom-anthropic-compatible','custom-openresponses-compatible'}
EXPECTED_CONNECTORS = set('acp agycliacp azure cloudflare codex cohere commandcode-anthropic commandcode-openai cursorcliacp cursorsdk databricks geminicliacp gitlabduo huggingface infomaniak llamacpp lmstudio localstub minimexoauth nousportal nvidia oci ollama opencode openrouter qwenoauth replicate sagemaker sapaicore snowflake vertex vllm watsonx xaioauth'.split())


def ceil(value: Fraction) -> int:
    return -(-value.numerator // value.denominator)


def validate_values(u: dict, policy: dict, fixtures: dict, manifest: dict) -> list[str]:
    errors = []
    def check(ok, message):
        if not ok:
            errors.append(message)
    tasks = {t['id']:t for t in manifest['tasks']}
    fids = [f['id'] for f in u['frontends']]
    bids = [b['id'] for b in u['backends']]
    check(set(fids) == {'frontend:'+x for x in EXPECTED_FRONTENDS} and len(fids)==5, 'frontend inventory mismatch')
    check(set(bids) == {'builtin:'+x for x in EXPECTED_BUILTINS}|{'connector:'+x for x in EXPECTED_CONNECTORS} and len(bids)==44, 'backend/connector inventory mismatch')
    check(u['content_atoms'] == [{'id':n,'bit':1<<i} for i,n in enumerate(['text','image','audio','video','document','binary'])], 'content vocabulary changed without revised coverage contract')
    check(u['base_input_masks']==list(range(64)) and u['base_output_masks']==list(range(64)), 'incomplete or duplicated mask product')
    check(u['baseline_pair_count']==220 and u['baseline_signature_count']==901120, 'baseline coordinate counts changed')
    check(u['implementation_verified'] is False, 'inventory falsely claims implementation verified')
    for entry in u['frontends']+u['backends']:
        check(entry['task'] in tasks, 'inventory entry has no implementation task: '+entry['id'])
        check(entry['support_status']=='implementation_not_verified', 'fabricated support certificate: '+entry['id'])
    check(policy['version']==3 and policy['spec_revision']==3, 'wrong coverage policy revision')
    check(policy['input_output_mask_product']=='complete' and policy['filter_before_enumeration'] is False, 'filtered or non-exhaustive product')
    check(policy['coordinate_classification']=='complete_before_capability_filtering', 'coordinate classification is not exhaustive')
    check(policy['evidence_model']=='factored_conformance' and policy['full_stack_coordinate_execution_required'] is False, 'revision 3 factored evidence model not active')
    check(policy['pairwise_is_release_sufficient'] is False, 'pairwise cannot substitute for exhaustive coverage')
    required_components={'frontend_contract_evidence','backend_profile_contract_evidence','common_financial_kernel_evidence','qualifying_real_stack_witness_evidence'}
    check(set(policy['proof_components'])==required_components, 'factored proof components incomplete')
    required_witnesses={'every_baseline_frontend_backend_pair_with_positive_intersection','every_distinct_backend_profile_api_transport_implementation','every_connector_actual_module_entrypoint','every_frontend_delivery_and_carrier_family','targeted_cross_boundary_mixed_and_financial_fault_schedules'}
    check(set(policy['required_real_stack_witnesses'])==required_witnesses, 'mandatory real-stack witness set incomplete')
    modes = policy['required_transport_mode_pairs']
    check(sorted(modes)==sorted([[a,b] for a in ['streaming','non_streaming'] for b in ['streaming','non_streaming']]), 'missing asymmetric mode combination')
    positive = policy['required_positive_rules']
    check({r['id'] for r in positive}=={f'P{i:02}' for i in range(1,10)} and len(positive)==9, 'missing positive obligations / all-deny specification')
    check(all(r['removable_by_executor'] is False and r['rule'] for r in positive), 'positive obligation may be removed or is empty')
    check(set(policy['dispositions'])=={'REQUIRED_SUPPORTED','NATIVE_UNREPRESENTABLE','STRICT_UNBOUNDED','BILLING_IMPLEMENTATION_GAP','RUNTIME_PREREQUISITE_UNAVAILABLE'}, 'gap/native distinction missing')
    must = {'missing_coordinate','missing_profile_expansion','missing_transport_expansion','missing_connector','skipped_case','not_run_case','implementation_gap','prerequisite_unavailable','all_deny','stale_digest','native_negative_without_proof','missing_frontend_contract_evidence','missing_backend_profile_evidence','missing_financial_kernel_evidence','missing_required_real_stack_witness','missing_connector_entrypoint_witness','unproven_evidence_composition'}
    check(must<=set(policy['release_fails_on']), 'release gate fails to reject incomplete or vacuous evidence')
    check(fixtures['prices_are_synthetic'] is True and fixtures['implementation_verified'] is False, 'fixture provenance invalid')
    rows = fixtures['fixtures']
    check(len(rows)==20 and {r['id'] for r in rows}=={f'FX{i:02}' for i in range(1,21)}, 'missing literal oracle fixture')
    for row in rows:
        supports=[a['support'] for a in row['atoms']]
        check(len(supports)==len(set(supports)), f'duplicate priced support: {row["id"]}')
        amounts=[]
        for atom in row['atoms']:
            q,r=Fraction(atom['quantity']),Fraction(atom['rate_nano_per_unit'])
            check(q>=0 and r>=0 and bool(atom['unit']), f'invalid exact unit value: {row["id"]}')
            amounts.append(q*r)
        if row['state']=='native_reject':
            check(row['expected_provider_starts']==0 and row['requested_output_set'] not in row['native_allowed_exact_output_sets'], 'invalid native-negative oracle')
            continue
        expected = sum(ceil(a) for a in amounts) if row['rounding']=='ceil_each_atom' else ceil(sum(amounts,Fraction(0)))
        if row['state']=='pending':
            check(row['expected_total_charge_nano'] is None and expected==row['expected_known_charge_nano'], 'pending ambiguity was converted to an invoice')
        else:
            check(expected==row['expected_charge_nano'], f'literal amount mismatch: {row["id"]}')
        check(row['reserved_upper_nano']>=expected, f'fixture bound below actual charge: {row["id"]}')
    return errors


def load(spec: Path):
    def read(p): return json.loads((spec/p).read_text(encoding='utf-8'))
    return (read('coverage/universe.json'), read('coverage/matrix-policy.json'),
            read('coverage/economic-fixtures.json'), read('execution/task-manifest.json'))


def self_test(values) -> dict:
    mutations = [
        ('missing connector',lambda u,p,f,m:u['backends'].pop()),
        ('missing Bedrock',lambda u,p,f,m:u['backends'].__setitem__(slice(None),[x for x in u['backends'] if x['id']!='builtin:bedrock'])),
        ('missing frontend',lambda u,p,f,m:u['frontends'].pop()),
        ('missing dense input',lambda u,p,f,m:u['base_input_masks'].remove(63)),
        ('missing empty output',lambda u,p,f,m:u['base_output_masks'].remove(0)),
        ('duplicated mask',lambda u,p,f,m:u['base_output_masks'].append(1)),
        ('filtered denominator',lambda u,p,f,m:p.__setitem__('filter_before_enumeration',True)),
        ('pairwise substitution',lambda u,p,f,m:p.__setitem__('pairwise_is_release_sufficient',True)),
        ('full-stack explosion restored',lambda u,p,f,m:p.__setitem__('full_stack_coordinate_execution_required',True)),
        ('missing proof component',lambda u,p,f,m:p['proof_components'].pop()),
        ('missing mandatory witness class',lambda u,p,f,m:p['required_real_stack_witnesses'].pop()),
        ('all deny',lambda u,p,f,m:p['required_positive_rules'].clear()),
        ('native flags replace obligations',lambda u,p,f,m:p['required_positive_rules'][0].__setitem__('removable_by_executor',True)),
        ('missing asymmetric mode',lambda u,p,f,m:p['required_transport_mode_pairs'].pop()),
        ('pending becomes zero',lambda u,p,f,m:f['fixtures'][18].__setitem__('expected_total_charge_nano',0)),
        ('wrong mixed literal',lambda u,p,f,m:f['fixtures'][1].__setitem__('expected_charge_nano',1)),
        ('fake runtime certificate',lambda u,p,f,m:u.__setitem__('implementation_verified',True)),
    ]
    baseline_errors=validate_values(*values)
    rejected=[];missed=[]
    for label,mutate in mutations:
        changed=copy.deepcopy(values)
        mutate(*changed)
        (rejected if validate_values(*changed) else missed).append(label)
    return {'status':'PASS' if not baseline_errors and not missed else 'FAIL',
            'kind':'NEGATIVE_SPECIFICATION_CHECKS_ONLY','mutations_rejected':rejected,
            'missed_mutations':missed,'baseline_errors':baseline_errors,'implementation_verified':False}


def main() -> int:
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec',type=Path,default=Path(__file__).resolve().parents[1])
    parser.add_argument('--self-test',action='store_true')
    args=parser.parse_args()
    try:
        values=load(args.spec)
        if args.self_test: result=self_test(values)
        else:
            errors=validate_values(*values)
            result={'status':'FAIL' if errors else 'PASS', 'kind':'SPECIFICATION_COVERAGE_AND_LITERAL_ARITHMETIC',
                    'base_signature_obligations':901120,'interface_pairs':220,'literal_fixtures':20,
                    'evidence_model':policy['evidence_model'],'native_support_execution':'NOT_RUN','implementation_verified':False,'errors':errors}
    except (OSError,ValueError,KeyError,TypeError,ZeroDivisionError) as exc:
        result={'status':'FAIL','implementation_verified':False,'errors':[str(exc)]}
    print(json.dumps(result,indent=2))
    return 0 if result['status']=='PASS' else 1


if __name__=='__main__':
    raise SystemExit(main())
