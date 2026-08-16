import assert from 'node:assert/strict';
import test from 'node:test';

import { imageContextForTurn } from './product-memory-extract.mjs';

test('LoCoMo image context preserves both the search text and generated description', () => {
  assert.equal(imageContextForTurn({
    query: 'eternal sunshine of the spotless mind movie',
    blip_caption: 'a photo of two people sitting on a train',
  }), 'Sharing image - query: eternal sunshine of the spotless mind movie. The image shows: a photo of two people sitting on a train');
  assert.equal(imageContextForTurn({ query: 'becoming nicole book' }),
    'Sharing image - query for: becoming nicole book');
  assert.equal(imageContextForTurn({ blip_caption: 'a book cover' }),
    'Sharing image that shows: a book cover');
  assert.equal(imageContextForTurn({}), '');
});
