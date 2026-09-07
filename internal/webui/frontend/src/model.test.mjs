import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mergeEvents, timelineEntries, plainTerminal, duration, parseCommandSummary, extractDiffBlocks, parseDiff, countDiffChanges, toolTitle, summaryOf, stripContext } from './model.ts';
const event = (sequence, rest = {}) => ({ sequence, workspace: 'test', type: 'agent.activity', created_at: '2026-09-06T10:00:00Z', ...rest });
test('SSE replay is deduplicated and ordered', () => {
  assert.deepEqual(mergeEvents([event(3), event(1)], [event(2), event(3, { summary: 'new' })]).map(item => [item.sequence, item.summary]), [[1, undefined], [2, undefined], [3, 'new']]);
});
test('long-running sessions keep a bounded event buffer', () => {
  assert.deepEqual(mergeEvents([event(1), event(2)], [event(3)], 2).map(item => item.sequence), [2, 3]);
});
test('tool completion updates the matching start, not another tool', () => {
  const entries = timelineEntries([event(1, { type: 'tool.started', call_id: 'a', input: { path: 'src' } }), event(2, { type: 'tool.started', call_id: 'b' }), event(3, { type: 'tool.completed', call_id: 'a', status: 'succeeded' })]);
  assert.equal(entries.length, 2); assert.equal(entries[0].status, 'succeeded'); assert.deepEqual(entries[0].input, { path: 'src' }); assert.equal(entries[1].call_id, 'b');
});
test('command output chunks merge into the owning tool card', () => {
  const entries = timelineEntries([
    event(1, { type: 'tool.started', call_id: 'a', tool: 'execute' }),
    event(2, { type: 'command.output', call_id: 'a', stream: 'stdout', output: { text: 'one\n' } }),
    event(3, { type: 'command.output', call_id: 'a', stream: 'stdout', output: { text: 'two' } }),
    event(4, { type: 'command.output', call_id: 'a', stream: 'stderr', output: { text: 'oops' } }),
    event(5, { type: 'tool.completed', call_id: 'a', status: 'succeeded' }),
  ]);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0].outputs, [{ stream: 'stdout', text: 'one\ntwo' }, { stream: 'stderr', text: 'oops' }]);
});
test('orphan command output still renders after the parent card', () => {
  const entries = timelineEntries([event(1, { type: 'tool.completed', call_id: 'a', status: 'succeeded' }), event(2, { type: 'command.output', call_id: 'late', stream: 'stdout', output: { text: 'tail' } })]);
  assert.equal(entries.length, 2); assert.equal(entries[1].outputs[0].text, 'tail');
});
test('file change diffs attach to the edit that produced them', () => {
  const entries = timelineEntries([
    event(1, { type: 'tool.started', call_id: 'e', tool: 'edit' }),
    event(2, { type: 'file.changed', call_id: 'e', path: 'a.go', output: { diff_summary: '--- a/a.go\n+++ b/a.go\n-old\n+new\n', paths: ['a.go'] } }),
    event(3, { type: 'tool.completed', call_id: 'e', status: 'succeeded' }),
  ]);
  assert.equal(entries.length, 1);
  assert.deepEqual(entries[0].diffs, ['--- a/a.go\n+++ b/a.go\n-old\n+new\n']);
  assert.deepEqual(entries[0].changed_paths, ['a.go']);
});
test('command summary parsing separates stdout and stderr sections', () => {
  const parsed = parseCommandSummary('Context:\n- purpose: demo\n\nCommand completed with exit code 0.\n\nstdout:\nline1\nline2\n\nstderr:\nwarn1\n');
  assert.equal(parsed.note, 'Command completed with exit code 0.');
  assert.equal(parsed.stdout, 'line1\nline2\n');
  assert.equal(parsed.stderr, 'warn1\n');
  const plain = parseCommandSummary('just a note');
  assert.equal(plain.note, 'just a note'); assert.equal(plain.stdout, undefined);
});
test('diff fences and unified diffs are parsed line by line', () => {
  const blocks = extractDiffBlocks('before\n```diff\n-a\n+b\n```\nafter');
  assert.deepEqual(blocks, ['-a\n+b']);
  assert.deepEqual(countDiffChanges(blocks), { added: 1, removed: 1 });
  const lines = parseDiff('--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n ctx');
  assert.deepEqual(lines.map(l => l.kind), ['meta', 'meta', 'hunk', 'del', 'add', 'ctx']);
});
test('tool titles expose upstream server and tool names', () => {
  assert.equal(toolTitle({ tool: 'execute' }), '执行命令');
  assert.equal(toolTitle({ tool: 'mcp_tool', mcp_server: 'zvec-grep', mcp_tool: 'zvec_grep_search' }), 'zvec-grep · zvec_grep_search');
  assert.equal(toolTitle({}), 'Runtime');
});
test('summaries prefer the nested human text and drop the context preamble', () => {
  const wrapped = event(1, { summary: 'short', output: { summary: 'Context:\n- purpose: p\n\nReal body' } });
  assert.equal(summaryOf(wrapped), 'Context:\n- purpose: p\n\nReal body');
  assert.equal(stripContext(summaryOf(wrapped)), 'Real body');
});
test('ANSI and OSC escape sequences are removed without HTML execution', () => {
  assert.equal(plainTerminal('\x1b[32mOK\x1b[0m\rnext\n\x1b]0;title\x07safe'), 'OK\nnext\nsafe');
  assert.equal(plainTerminal('<script>not markup</script>'), '<script>not markup</script>');
});
test('duration remains readable', () => { assert.equal(duration(1000), '1.0s'); assert.equal(duration(61000), '1m 1s'); });
