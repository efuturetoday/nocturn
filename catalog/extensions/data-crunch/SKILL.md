---
name: data-crunch
description: Compute totals, averages, counts or groupings over a CSV or JSON file in the workspace. Use when the user asks a numeric question about a data file rather than about its contents.
---
# Crunch a data file

Numbers come out of an interpreter, never out of reading a file and estimating. A model that eyeballs
a thousand rows gets a plausible answer, and plausible is the failure mode that does not announce
itself.

1. Find the file: `file_search` with a glob (`*.csv`, `*.json`) or `file_list` when the user named a
   folder. Confirm which file you are about to use if more than one matches.
2. Learn the shape, not the data. `file_read` it and look at the header and the first rows only —
   column names, separator, decimal comma or point, quoting, whether numbers carry a currency symbol
   or a thousands separator. A large file does not belong in the conversation.
3. Compute with `code_run`. Read the file inside the script — the workspace file tools are available
   to it as `nocturn.fs`:

   ```js
   const raw = await nocturn.fs.readFile("data/sales.csv");
   const rows = raw.trim().split("\n").slice(1).map(l => l.split(","));
   const total = rows.reduce((sum, r) => sum + Number(r[3]), 0);
   console.log(JSON.stringify({ rows: rows.length, total }));
   ```

   Print a small result, not the rows. What the script writes to stdout is what comes back.
4. Report the number with the count it came from ("48,120 across 213 rows"), and name every row you
   dropped and why — unparseable, empty, filtered out. A total that silently skipped 12 rows is wrong
   in the way nobody catches.
5. Write results back with `nocturn.fs.writeFile` (or `file_write`) only when the user asked for a
   file. That is a write, and it may need approval.

If the file is not what the question needs — wrong columns, wrong period — say that instead of
computing the closest available thing and presenting it as the answer.
