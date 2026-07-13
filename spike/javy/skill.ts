// skill.ts — a pure-compute "code-interpreter" skill: JSON in -> JSON out.
// No capabilities: no network, no filesystem, no clock. This is the safest
// possible guest — zero ambient authority is exactly enough. Authored in
// TypeScript, transpiled to JS (esbuild), compiled to wasm (javy), and run on
// our OWN raw wazero host with only bounded stdio granted.
declare const Javy: {
	IO: {
		readSync(fd: number, buf: Uint8Array): number;
		writeSync(fd: number, buf: Uint8Array): number;
	};
};

interface Input {
	name: string;
	numbers: number[];
}

function readAllStdin(): Uint8Array {
	const chunks: Uint8Array[] = [];
	const buf = new Uint8Array(1024);
	let total = 0;
	for (;;) {
		const n = Javy.IO.readSync(0, buf);
		if (n === 0) break;
		chunks.push(buf.slice(0, n));
		total += n;
	}
	const out = new Uint8Array(total);
	let off = 0;
	for (const c of chunks) {
		out.set(c, off);
		off += c.length;
	}
	return out;
}

function writeStdout(bytes: Uint8Array): void {
	Javy.IO.writeSync(1, bytes);
}

const input: Input = JSON.parse(new TextDecoder().decode(readAllStdin()));
const result = {
	greeting: `hello, ${input.name}`,
	sum: input.numbers.reduce((a, b) => a + b, 0),
	max: Math.max(...input.numbers),
};
writeStdout(new TextEncoder().encode(JSON.stringify(result)));
