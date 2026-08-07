import assert from 'node:assert/strict';
import test from 'node:test';

import { renderLocalMergeReview } from './local-merge-review.js';

test('local merge review keeps conflict text inert and emits a reviewed download', () => {
	const html = renderLocalMergeReview({
		schema: 'pulse.local_store_merge_preview.v1',
		totals: { events_created: 2, capsules_created: 3, events_deduplicated: 4 },
		conflicts: [{
			id: 'one', kind: 'assertion', selected: '',
			choices: [
				{ id: 'current', label: '</script><script>bad()</script>' },
				{ id: 'imported', label: 'Новое значение' },
			],
		}],
	}, '/tmp/personal-preview.json');
	assert.match(html, /Собираем личную память/);
	assert.match(html, /personal-preview\.reviewed\.json/);
	assert.match(html, /merge local pulse memory/);
	assert.doesNotMatch(html, /<script>bad\(\)<\/script>/);
});

test('local merge review rejects another file type', () => {
	assert.throws(() => renderLocalMergeReview({ schema: 'other' }, 'preview.json'), /unsupported/);
});
