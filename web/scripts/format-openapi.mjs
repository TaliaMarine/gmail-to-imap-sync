// Reads an OpenAPI document on stdin and writes it pretty printed, so the
// checked-in snapshot diffs cleanly.
let input = ''
for await (const chunk of process.stdin) input += chunk
process.stdout.write(JSON.stringify(JSON.parse(input), null, 2) + '\n')
