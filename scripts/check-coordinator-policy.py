#!/usr/bin/env python3
"""Check Coordinator policy provenance and dependency structure, not model behavior."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
POLICY = ROOT / 'server/internal/service/inboundcoord/policy'
INVENTORY = ROOT / 'docs/plans/2026-09-07-coordinator-progressive-context-inventory.json'


def check() -> dict[str, object]:
    registry = json.loads((POLICY / 'registry.json').read_text())
    cases = json.loads((POLICY / 'cases.json').read_text())['cases']
    inventory = json.loads(INVENTORY.read_text())
    errors: list[str] = []

    def require(condition: object, message: str) -> None:
        if not condition:
            errors.append(message)

    def unique(rows: list[dict], label: str) -> dict[str, dict]:
        result = {}
        for row in rows:
            row_id = row.get('id', '')
            require(row_id and row_id not in result, f'{label}: missing/duplicate id {row_id}')
            result[row_id] = row
        return result

    modules = unique(registry['modules'], 'module')
    rules = unique(registry['obligations'], 'rule')
    case_map = unique(cases, 'case')
    entries = unique(inventory['entries'], 'source')
    require({r['family'] for r in rules.values()} == set(inventory['families']), 'historical family coverage is incomplete')
    require(len(entries) == 103, 'observed source inventory must retain all 103 original entries')
    require(sum(e['kind'] == 'bullet' for e in entries.values()) == 99, 'expected 99 observed bullets')
    require(sum(e['kind'] == 'preamble' for e in entries.values()) == 4, 'expected four observed preambles')
    allowed_conditions = {'always', 'inbound', 'dingtalk', 'web', 'group', 'multiple_utterances',
                          'scene_memory_available', 'skill_snapshots_available', 'dialogue_available',
                          'recalled', 'task_finished'}
    for module_id, module in modules.items():
        file = POLICY / module['file']
        require(file.is_file(), f'{module_id}: body file missing')
        require(module['applies_when'] in allowed_conditions, f'{module_id}: unregistered selection condition')
        require(bool(module.get('version')), f'{module_id}: version missing')
        if file.is_file():
            require(hashlib.sha256(file.read_text().strip().encode()).hexdigest() == module.get('content_sha256'), f'{module_id}: body changed without registered provenance update')
            require(len(file.read_text().strip()) <= module['budget_characters'], f'{module_id}: module exceeds declared character budget')
        for dep in module['requires']:
            require(dep in modules, f'{module_id}: dependency {dep} missing')
        for rule_id in module['owns_rule_ids']:
            require(rule_id in rules, f'{module_id}: unknown rule {rule_id}')
    visiting: set[str] = set()
    visited: set[str] = set()

    def visit(module_id: str) -> None:
        if module_id in visiting:
            errors.append(f'cyclic module dependency: {module_id}')
            return
        if module_id in visited or module_id not in modules:
            return
        visiting.add(module_id)
        for dep in modules[module_id]['requires']:
            visit(dep)
        visiting.remove(module_id)
        visited.add(module_id)

    for module_id in modules:
        visit(module_id)
    for rule_id, rule in rules.items():
        require(rule_id.startswith('COORD.'), f'{rule_id}: unregistered namespace')
        require(bool(rule.get('obligation')), f'{rule_id}: behavior obligation missing')
        require(bool(rule.get('implementation_refs')), f'{rule_id}: implementation home missing')
        require(bool(rule.get('case_ids')), f'{rule_id}: contrast case missing')
        for module_id in rule.get('runtime_modules', []):
            require(module_id in modules, f'{rule_id}: unknown runtime module {module_id}')
            require(rule_id in modules.get(module_id, {}).get('owns_rule_ids', []), f'{rule_id}: module ownership is not reciprocal')
        for case_id in rule.get('case_ids', []):
            require(case_id in case_map, f'{rule_id}: missing case {case_id}')
        for ref in rule.get('implementation_refs', []):
            require((ROOT / ref.split('#')[0]).is_file(), f'{rule_id}: implementation reference does not exist: {ref}')
    for entry_id, entry in entries.items():
        source_line = ('- ' if entry['kind'] == 'bullet' else '') + entry['text']
        digest = hashlib.sha256(source_line.encode()).hexdigest()
        require(digest == entry['text_sha256'], f'{entry_id}: immutable source text/hash changed')
        mapping = entry.get('implementation', {})
        require(mapping.get('policy_version') == registry['policy_version'], f'{entry_id}: mapping policy version mismatch')
        require(bool(mapping.get('rule_ids')), f'{entry_id}: source has no behavior obligation')
        require(mapping.get('coverage') == 'source_mapping_only', f'{entry_id}: structural mapping must not claim behavior certification')
        for rule_id in mapping.get('rule_ids', []):
            require(rule_id in rules, f'{entry_id}: references unknown obligation {rule_id}')
        for module_id in mapping.get('modules', []):
            require(module_id in modules, f'{entry_id}: references unknown module {module_id}')
    require({'work_submission', 'task_finished_reply', 'direct_reply'} <= set(registry['effect_dependencies']), 'required effect boundary missing')
    for effect, deps in registry['effect_dependencies'].items():
        require(bool(deps), f'{effect}: missing prerequisites')
        for dep in deps:
            require(dep in modules, f'{effect}: unknown dependency {dep}')
    for retired in registry.get('retired_implementations', []):
        require(retired.get('status') == 'superseded' and retired.get('reason'), f'{retired["id"]}: retirement lacks a reason')
        require(bool(retired.get('superseded_by')), f'{retired["id"]}: missing successor obligation')
        for rule_id in retired.get('superseded_by', []):
            require(rule_id in rules, f'{retired["id"]}: unknown successor {rule_id}')
    prompt = (ROOT / 'server/internal/service/inboundcoord/prompt.go').read_text()
    require(not re.search(r'const\s+systemPrompt\s*=\s*`', prompt), 'monolithic systemPrompt bypasses the module registry')
    contract = (ROOT / 'docs/inbound-coordinator-loop.md').read_text()
    require(f'policy_version: `{registry["policy_version"]}`' in contract, 'current contract policy version mismatch')
    if errors:
        raise ValueError('\n'.join(errors))
    return {'status': 'PASS_STRUCTURAL_ONLY', 'policy_version': registry['policy_version'],
            'source_entries': len(entries), 'obligations': len(rules), 'modules': len(modules),
            'contrast_contracts': len(case_map), 'behavior_verification': 'not certified by this checker'}


if __name__ == '__main__':
    try:
        print(json.dumps(check(), ensure_ascii=False))
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f'Coordinator policy structure FAILED:\n{error}', file=sys.stderr)
        sys.exit(1)
