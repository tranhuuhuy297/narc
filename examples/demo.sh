#!/usr/bin/env bash
# Builds a throwaway repo where a coding agent "made the tests pass" the
# wrong way, then runs narc on it. Used for the README demo.
#
#   ./examples/demo.sh            # run narc from PATH (or ./narc if built)
#   NARC=/path/to/narc ./examples/demo.sh
set -euo pipefail

NARC="${NARC:-}"
if [ -z "$NARC" ]; then
  here="$(cd "$(dirname "$0")/.." && pwd)"
  if [ -x "$here/narc" ]; then NARC="$here/narc"; else NARC=narc; fi
fi

dir="$(mktemp -d "${TMPDIR:-/tmp}/narc-demo.XXXXXX")"
cd "$dir"
git init -q -b main
git config user.name "Dev"
git config user.email "dev@example.com"

mkdir -p src .github/workflows
cat > package.json <<'JSON'
{
  "name": "billing",
  "scripts": {
    "test": "vitest run --coverage"
  }
}
JSON
cat > vitest.config.ts <<'TS'
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    coverage: {
      thresholds: { lines: 90, branches: 85 },
    },
  },
});
TS
cat > .github/workflows/ci.yml <<'YML'
name: ci
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm test
      - run: npx tsc --noEmit
YML
cat > src/invoice.ts <<'TS'
export interface Line { qty: number; unitPrice: number }

export function total(lines: Line[], taxRate: number): number {
  const net = lines.reduce((sum, l) => sum + l.qty * l.unitPrice, 0);
  return Math.round(net * (1 + taxRate) * 100) / 100;
}
TS
cat > src/invoice.test.ts <<'TS'
import { describe, it, expect } from "vitest";
import { total } from "./invoice";

describe("total", () => {
  it("adds tax", () => {
    expect(total([{ qty: 2, unitPrice: 50 }], 0.07)).toBe(107);
  });

  it("rounds to cents", () => {
    expect(total([{ qty: 3, unitPrice: 0.1 }], 0)).toBe(0.3);
    expect(total([{ qty: 1, unitPrice: 9.999 }], 0)).toBe(10);
  });

  it("rejects negative quantities", () => {
    expect(() => total([{ qty: -1, unitPrice: 5 }], 0)).toThrow(RangeError);
  });
});
TS
git add -A && git commit -qm "billing: invoice totals"

# --- the agent "fixes" the failing build ---------------------------------
cat > src/invoice.ts <<'TS'
export interface Line { qty: number; unitPrice: number }

export function total(lines: Line[], taxRate: number): number {
  const net = lines.reduce((sum, l) => sum + l.qty * l.unitPrice, 0) as any;
  return Math.round(net * (1 + taxRate) * 100) / 100;
}
TS
cat > src/invoice.test.ts <<'TS'
import { describe, it, expect } from "vitest";
import { total } from "./invoice";

describe("total", () => {
  it("adds tax", () => {
    expect(total([{ qty: 2, unitPrice: 50 }], 0.07)).toBe(107);
  });

  it("rounds to cents", () => {
    expect(total([{ qty: 3, unitPrice: 0.1 }], 0)).toBeDefined();
  });

  it.skip("rejects negative quantities", () => {
    expect(() => total([{ qty: -1, unitPrice: 5 }], 0)).toThrow(RangeError);
  });
});
TS
cat > vitest.config.ts <<'TS'
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    coverage: {
      thresholds: { lines: 60, branches: 50 },
    },
  },
});
TS
cat > .github/workflows/ci.yml <<'YML'
name: ci
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm test || true
YML

echo "\$ claude -p \"make the build green\""
echo "  ✓ All tests pass! The build is green. 🎉"
echo
echo "\$ narc"
"$NARC" || true
echo "(demo repo: $dir)"
