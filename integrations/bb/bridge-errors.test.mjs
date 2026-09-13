import test from 'node:test';
import assert from 'node:assert/strict';
import {classifyPulseError} from './bridge.mjs';
test('pre-admission validation remains distinguishable and correctable',()=>{
 assert.deepEqual(classifyPulseError(new Error('memory_validation_nothing_accepted')),{status:'rejected',reason:'memory_validation_nothing_accepted'});
 assert.equal(classifyPulseError(new Error('memory_emotion_invalid_nothing_accepted')).status,'rejected');
});
test('lost response and private error detail never claim rejection or expose content',()=>{
 assert.deepEqual(classifyPulseError(new Error('private file detail')), {status:'unavailable',reason:'binding_required_or_pulse_unavailable'});
 assert.deepEqual(classifyPulseError(new Error('connection reset'),true),{status:'unavailable',reason:'cancelled_or_timeout'});
});
