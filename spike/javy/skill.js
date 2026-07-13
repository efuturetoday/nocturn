(() => {
  function readAllStdin() {
    const chunks = [];
    const buf = new Uint8Array(1024);
    let total = 0;
    for (; ; ) {
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
  function writeStdout(bytes) {
    Javy.IO.writeSync(1, bytes);
  }
  const input = JSON.parse(new TextDecoder().decode(readAllStdin()));
  const result = {
    greeting: `hello, ${input.name}`,
    sum: input.numbers.reduce((a, b) => a + b, 0),
    max: Math.max(...input.numbers)
  };
  writeStdout(new TextEncoder().encode(JSON.stringify(result)));
})();
