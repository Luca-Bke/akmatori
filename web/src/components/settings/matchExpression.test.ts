import { describe, it, expect } from 'vitest';
import { substituteUUIDsForDisplay, validateMatchExpression } from './matchExpression';

describe('validateMatchExpression', () => {
  const valid = [
    '',
    '   ',
    'skill == "netbox"',
    "skill == 'netbox'",
    'skill = "netbox"',
    'SKILL != "netbox"',
    'last_skill == "netbox"',
    'source_kind == "alert" && channel == "c-1"',
    'source_kind == "alert" AND (channel == "c-1" OR skill == "x")',
    '!(source_kind == "cron")',
    'not source_kind == "cron"',
    '!skill == "x" || trigger == "t-1"',
  ];
  for (const expr of valid) {
    it(`accepts: ${expr || '(empty)'}`, () => {
      expect(validateMatchExpression(expr)).toBeNull();
    });
  }

  const invalid: Array<[string, string]> = [
    ['bogus == "x"', 'unknown field'],
    ['skill "x"', 'expected == or !='],
    ['skill == netbox', 'must be quoted'],
    ['skill == "netbox', 'unterminated string'],
    ['(skill == "x"', 'missing closing parenthesis'],
    ['skill == "a" && ', 'expected a condition'],
    ['skill == "a" skill == "b"', 'unexpected'],
    ['&& skill == "a"', 'expected a field name'],
  ];
  for (const [expr, msg] of invalid) {
    it(`rejects: ${expr}`, () => {
      const error = validateMatchExpression(expr);
      expect(error).not.toBeNull();
      expect(error).toContain(msg);
      expect(error).toContain('position');
    });
  }
});

describe('substituteUUIDsForDisplay', () => {
  it('replaces known quoted values and leaves unknown ones', () => {
    const out = substituteUUIDsForDisplay('channel == "c-1" && skill == "netbox"', {
      'c-1': '#alerts',
    });
    expect(out).toBe('channel == "#alerts" && skill == "netbox"');
  });

  it('handles single quotes', () => {
    expect(substituteUUIDsForDisplay("channel == 'c-1'", { 'c-1': '#a' })).toBe(
      "channel == '#a'",
    );
  });
});
