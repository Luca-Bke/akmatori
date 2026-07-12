import { describe, it, expect } from 'vitest';
import {
  MATCH_SOURCE_KINDS,
  buildRulePayload,
  emptyRuleFormState,
  moveInList,
  ruleConditionSummary,
  ruleFormStateFromRule,
} from './formattingRulesHelpers';
import {
  DEFAULT_FORMATTING_PROMPT_PLACEHOLDER,
  DEFAULT_OUTPUT_SCHEMA_EXAMPLE,
} from './formattingSettingsHelpers';
import type { FormattingRule } from '../../types';

function makeRule(overrides: Partial<FormattingRule> = {}): FormattingRule {
  return {
    id: 1,
    uuid: 'rule-uuid',
    name: 'test rule',
    enabled: true,
    position: 0,
    match_source_kind: '',
    match_source_uuid: '',
    match_channel_uuid: '',
    match_last_skill: '',
    match_expression: '',
    system_prompt: '',
    output_schema_example: '',
    max_tokens: 1500,
    temperature: 0.2,
    created_at: '',
    updated_at: '',
    ...overrides,
  };
}

describe('MATCH_SOURCE_KINDS', () => {
  it('excludes proposal (formatter never runs on proposal chat)', () => {
    expect(MATCH_SOURCE_KINDS.some((k) => k.value === 'proposal')).toBe(false);
  });

  it('starts with the wildcard option', () => {
    expect(MATCH_SOURCE_KINDS[0].value).toBe('');
  });
});

describe('ruleFormStateFromRule / buildRulePayload round-trip', () => {
  it('hydrates stored blanks to editable defaults', () => {
    const state = ruleFormStateFromRule(makeRule());
    expect(state.systemPrompt).toBe(DEFAULT_FORMATTING_PROMPT_PLACEHOLDER);
    expect(state.outputSchemaExample).toBe(DEFAULT_OUTPUT_SCHEMA_EXAMPLE);
  });

  it('dehydrates untouched defaults back to empty strings', () => {
    const payload = buildRulePayload(ruleFormStateFromRule(makeRule()));
    expect(payload.system_prompt).toBe('');
    expect(payload.output_schema_example).toBe('');
  });

  it('preserves custom prompt and schema edits', () => {
    const state = ruleFormStateFromRule(
      makeRule({ system_prompt: 'custom', output_schema_example: '{"a":1}' }),
    );
    const payload = buildRulePayload(state);
    expect(payload.system_prompt).toBe('custom');
    expect(payload.output_schema_example).toBe('{"a":1}');
  });

  it('trims name and match fields', () => {
    const state = emptyRuleFormState();
    state.name = '  spaced  ';
    state.matchLastSkill = ' netbox ';
    const payload = buildRulePayload(state);
    expect(payload.name).toBe('spaced');
    expect(payload.match_last_skill).toBe('netbox');
  });
});

describe('ruleConditionSummary', () => {
  it('labels a catch-all rule', () => {
    expect(ruleConditionSummary(makeRule())).toEqual(['matches everything']);
  });

  it('renders one chip per condition, resolving lookups', () => {
    const rule = makeRule({
      match_source_kind: 'alert',
      match_source_uuid: 'trigger-1',
      match_channel_uuid: 'chan-1',
      match_last_skill: 'netbox',
    });
    const chips = ruleConditionSummary(rule, {
      triggers: { 'trigger-1': 'Zabbix prod' },
      channels: { 'chan-1': '#alerts' },
    });
    expect(chips).toEqual([
      'kind: Alert',
      'trigger: Zabbix prod',
      'channel: #alerts',
      'skill: netbox',
    ]);
  });

  it('falls back to a shortened UUID when the lookup misses', () => {
    const chips = ruleConditionSummary(
      makeRule({ match_channel_uuid: '12345678-abcd-efgh' }),
    );
    expect(chips).toEqual(['channel: 12345678…']);
  });
});

describe('moveInList', () => {
  it('moves items up and down', () => {
    expect(moveInList(['a', 'b', 'c'], 2, -1)).toEqual(['a', 'c', 'b']);
    expect(moveInList(['a', 'b', 'c'], 0, 1)).toEqual(['b', 'a', 'c']);
  });

  it('returns the same list when the move is out of range', () => {
    const list = ['a', 'b'];
    expect(moveInList(list, 0, -1)).toBe(list);
    expect(moveInList(list, 1, 1)).toBe(list);
  });
});

describe('expression mode', () => {
  it('detects expression mode when hydrating a rule with an expression', () => {
    const state = ruleFormStateFromRule(
      makeRule({ match_expression: 'skill == "netbox"' }),
    );
    expect(state.matchMode).toBe('expression');
    expect(state.matchExpression).toBe('skill == "netbox"');
  });

  it('defaults to simple mode without an expression', () => {
    expect(ruleFormStateFromRule(makeRule()).matchMode).toBe('simple');
  });

  it('clears simple fields in the payload when in expression mode', () => {
    const state = ruleFormStateFromRule(
      makeRule({ match_source_kind: 'alert', match_last_skill: 'netbox' }),
    );
    state.matchMode = 'expression';
    state.matchExpression = 'channel == "c-1"';
    const payload = buildRulePayload(state);
    expect(payload.match_expression).toBe('channel == "c-1"');
    expect(payload.match_source_kind).toBe('');
    expect(payload.match_last_skill).toBe('');
  });

  it('clears the expression in the payload when in simple mode', () => {
    const state = ruleFormStateFromRule(makeRule({ match_expression: 'skill == "x"' }));
    state.matchMode = 'simple';
    state.matchSourceKind = 'cron';
    const payload = buildRulePayload(state);
    expect(payload.match_expression).toBe('');
    expect(payload.match_source_kind).toBe('cron');
  });

  it('summarizes an expression rule as a single chip with names substituted', () => {
    const chips = ruleConditionSummary(
      makeRule({ match_expression: 'channel == "chan-1" || skill == "netbox"' }),
      { channels: { 'chan-1': '#alerts' } },
    );
    expect(chips).toEqual(['expr: channel == "#alerts" || skill == "netbox"']);
  });
});
