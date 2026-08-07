import type { IncomingMessage, ServerResponse } from 'node:http';

interface FetchHandler {
	fetch(request: Request): Promise<Response>;
}

async function toWebRequest(req: IncomingMessage, signal: AbortSignal): Promise<Request> {
	const method = (req.method ?? 'GET').toUpperCase();
	const headers = new Headers();
	for (const [name, value] of Object.entries(req.headers)) {
		if (value === undefined) continue;
		if (Array.isArray(value)) {
			for (const item of value) headers.append(name, item);
		} else {
			headers.set(name, value);
		}
	}
	let body: string | undefined;
	if (method !== 'GET' && method !== 'HEAD') {
		const chunks: Uint8Array[] = [];
		for await (const chunk of req) {
			chunks.push(Buffer.from(chunk));
		}
		if (chunks.length > 0) body = Buffer.concat(chunks).toString('utf8');
	}
	return new Request(`http://${req.headers.host ?? '127.0.0.1'}${req.url ?? '/'}`, {
		method,
		headers,
		signal,
		...(body === undefined ? {} : { body }),
	});
}

// Personal exposes this adapter only on loopback in explicit development
// mode. Keeping the conversion here avoids shipping an unrelated static-file
// server merely to bridge Node HTTP and Web Request objects.
export function nodeHttpHandler(handler: FetchHandler) {
	return async (req: IncomingMessage, res: ServerResponse): Promise<void> => {
		let finished = false;
		const abort = new AbortController();
		res.on('close', () => {
			if (!finished) abort.abort();
		});
		try {
			const response = await handler.fetch(await toWebRequest(req, abort.signal));
			const headers: Record<string, string> = {};
			for (const [name, value] of response.headers) headers[name] = value;
			res.writeHead(response.status, headers);
			if (response.body !== null) {
				for await (const chunk of response.body) {
					if (abort.signal.aborted) break;
					if (!res.write(chunk)) await new Promise<void>((resolve) => res.once('drain', resolve));
				}
			}
			finished = true;
			res.end();
		} catch {
			if (!res.headersSent) res.writeHead(500, { 'Content-Type': 'application/json' });
			finished = true;
			res.end(JSON.stringify({
				jsonrpc: '2.0', id: null,
				error: { code: -32603, message: 'Internal server error' },
			}));
		}
	};
}
