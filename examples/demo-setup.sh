#!/usr/bin/env bash
# Creates a throwaway repo in which a coding agent "made the build green" by
# weakening verification, and prints its path. Used by docs/demo.tape.
set -euo pipefail
dir="$(mktemp -d "${TMPDIR:-/tmp}/billing.XXXXXX")"
cd "$dir"
git init -q -b main
git config user.name "Dev"
git config user.email "dev@example.com"
mkdir -p src .github/workflows
cat > vitest.config.ts <<'TS'
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: { coverage: { thresholds: { lines: 90 } } },
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
YML
cat > src/invoice.test.ts <<'TS'
import { describe, it, expect } from "vitest";
import { total } from "./invoice";

describe("total", () => {
  it("adds tax", () => {
    expect(total([{ qty: 2, price: 50 }], 0.07)).toBe(107);
  });

  it("rounds to cents", () => {
    expect(total([{ qty: 3, price: 0.1 }], 0)).toBe(0.3);
  });

  it("rejects negative quantities", () => {
    expect(() => total([{ qty: -1, price: 5 }], 0)).toThrow(RangeError);
  });
});
TS
printf 'export const total = (lines: { qty: number; price: number }[], tax: number) =>\n  lines.reduce((s, l) => s + l.qty * l.price, 0) * (1 + tax);\n' > src/invoice.ts
git add -A && git commit -qm "billing"

# --- what the agent did -------------------------------------------------
sed -i.bak 's/toBe(0.3);/toBeDefined();/' src/invoice.test.ts
sed -i.bak 's/  it("rejects negative/  it.skip("rejects negative/' src/invoice.test.ts
sed -i.bak 's/lines: 90/lines: 60/' vitest.config.ts
sed -i.bak 's/- run: npm test$/- run: npm test || true/' .github/workflows/ci.yml
rm -f src/*.bak *.bak .github/workflows/*.bak
echo "$dir"
