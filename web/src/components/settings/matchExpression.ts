// Client-side validator for formatting-rule match expressions. Mirrors the Go
// grammar in internal/services/formatting_expression.go:
//
//   expr       := orExpr
//   orExpr     := andExpr (("||" | "or") andExpr)*
//   andExpr    := unaryExpr (("&&" | "and") unaryExpr)*
//   unaryExpr  := ("!" | "not") unaryExpr | primary
//   primary    := "(" expr ")" | comparison
//   comparison := field ("==" | "!=" | "=") stringLiteral
//
// Fields (case-insensitive): source_kind, trigger, channel, skill
// (last_skill is accepted as an alias). Values are single- or double-quoted.
// The backend remains authoritative — this exists for instant feedback while
// typing in the rule editor.

export const EXPRESSION_FIELDS = ['source_kind', 'trigger', 'channel', 'skill'] as const;

const KNOWN_FIELDS = new Set<string>([...EXPRESSION_FIELDS, 'last_skill']);

class ExprParser {
  private pos = 0;
  private readonly input: string;

  constructor(input: string) {
    this.input = input;
  }

  fail(message: string): never {
    throw new Error(`position ${this.pos + 1}: ${message}`);
  }

  skipSpaces() {
    while (this.pos < this.input.length && /\s/.test(this.input[this.pos])) {
      this.pos++;
    }
  }

  peekSnippet(): string {
    const rest = this.input.slice(this.pos);
    return rest.length > 12 ? `${rest.slice(0, 12)}…` : rest;
  }

  tryKeyword(kw: string): boolean {
    this.skipSpaces();
    const end = this.pos + kw.length;
    if (this.input.slice(this.pos, end).toLowerCase() !== kw) return false;
    if (end < this.input.length && /[A-Za-z0-9_]/.test(this.input[end])) return false;
    this.pos = end;
    return true;
  }

  trySymbol(sym: string): boolean {
    this.skipSpaces();
    if (this.input.startsWith(sym, this.pos)) {
      this.pos += sym.length;
      return true;
    }
    return false;
  }

  parse(): void {
    this.parseOr();
    this.skipSpaces();
    if (this.pos < this.input.length) {
      this.fail(`unexpected "${this.peekSnippet()}" — expected && / || or end of expression`);
    }
  }

  parseOr(): void {
    this.parseAnd();
    while (this.trySymbol('||') || this.tryKeyword('or')) {
      this.parseAnd();
    }
  }

  parseAnd(): void {
    this.parseUnary();
    while (this.trySymbol('&&') || this.tryKeyword('and')) {
      this.parseUnary();
    }
  }

  parseUnary(): void {
    this.skipSpaces();
    const isBangNot =
      this.input.startsWith('!', this.pos) && !this.input.startsWith('!=', this.pos);
    if (isBangNot) {
      this.pos++;
      this.parseUnary();
      return;
    }
    if (this.tryKeyword('not')) {
      this.parseUnary();
      return;
    }
    this.parsePrimary();
  }

  parsePrimary(): void {
    if (this.trySymbol('(')) {
      this.parseOr();
      if (!this.trySymbol(')')) {
        this.fail('missing closing parenthesis');
      }
      return;
    }
    this.parseComparison();
  }

  parseComparison(): void {
    this.skipSpaces();
    if (this.pos >= this.input.length) {
      this.fail('expected a condition like: skill == "netbox"');
    }
    const start = this.pos;
    while (this.pos < this.input.length && /[A-Za-z0-9_]/.test(this.input[this.pos])) {
      this.pos++;
    }
    const word = this.input.slice(start, this.pos);
    if (!word) {
      this.fail(
        `expected a field name (${EXPRESSION_FIELDS.join(', ')}), got "${this.peekSnippet()}"`,
      );
    }
    if (!KNOWN_FIELDS.has(word.toLowerCase())) {
      this.pos = start;
      this.fail(`unknown field "${word}" — valid fields: ${EXPRESSION_FIELDS.join(', ')}`);
    }

    if (!this.trySymbol('==') && !this.trySymbol('!=') && !this.trySymbol('=')) {
      this.fail(`expected == or != after "${word}"`);
    }

    this.parseStringLiteral();
  }

  parseStringLiteral(): void {
    this.skipSpaces();
    if (this.pos >= this.input.length) {
      this.fail('expected a quoted value, e.g. "netbox"');
    }
    const quote = this.input[this.pos];
    if (quote !== '"' && quote !== "'") {
      this.fail(`value must be quoted, e.g. "netbox" — got "${this.peekSnippet()}"`);
    }
    this.pos++;
    while (this.pos < this.input.length && this.input[this.pos] !== quote) {
      this.pos++;
    }
    if (this.pos >= this.input.length) {
      this.fail(`unterminated string — missing closing ${quote}`);
    }
    this.pos++;
  }
}

// validateMatchExpression returns a user-facing error message, or null when
// the expression parses (empty expressions are valid — no expression set).
export function validateMatchExpression(input: string): string | null {
  if (!input.trim()) return null;
  try {
    new ExprParser(input).parse();
    return null;
  } catch (e) {
    return e instanceof Error ? e.message : String(e);
  }
}

// substituteUUIDsForDisplay replaces quoted UUID literals with quoted display
// names so expressions read naturally in the rule list. Purely cosmetic —
// never written back to the stored expression.
export function substituteUUIDsForDisplay(
  expression: string,
  names: Record<string, string>,
): string {
  return expression.replace(/(["'])([^"']*)\1/g, (match, quote: string, value: string) => {
    const name = names[value];
    return name ? `${quote}${name}${quote}` : match;
  });
}
