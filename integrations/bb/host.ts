import { experimental_defineHostEntry } from '@get-bb/plugin-sdk/host';
import { pulseContract } from './contract.ts';
import { callPulse } from './bridge.mjs';

export default experimental_defineHostEntry({
  contract: pulseContract,
  handlers: {
    call: (input, context) => callPulse(input, context.signal, context.lifecycle.signal),
  },
});
