import assert from 'node:assert/strict';
import test from 'node:test';
import { memoryInput } from './contract.ts';
for (const count of [4, 21, 61]) {
    test(`BB admits all ${count} requested parts without a selection quota`, () => {
        const items = Array.from({ length: count }, (_, index) => ({
            kind: 'emotion', scope: 'personal_global',
            summary: `Part ${index} contains several coexisting feelings.`, emotions: [
                { label: 'joy', name: 'Quiet gladness', intensity: 0.7, source: 'user' },
                { label: 'sadness', name: 'A little sadness', intensity: 0.3, source: 'inferred' },
            ]
        }));
        const parsed = memoryInput.parse({ items });
        assert.equal(parsed.items.length, count);
        assert.ok(parsed.items.every(item => item.emotions?.length === 2));
    });
}
test('BB preserves a long structured Russian summary', () => {
    const summary = 'Важные подробности чувства и его причины. '.repeat(100);
    assert.equal(memoryInput.parse({ items: [{ kind: 'relationship_note', summary }] }).items[0].summary, summary.trim());
});
