import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sortSessions, sortWorkspaces, isWorking, sessionTarget, workspaceTarget, canDrop, workingLabel } from './sidebar-model.ts';
const session=(id,extra={})=>({id,workspace:'alpha',label:id,description:'',status:'active',last_active_at:100,running_tasks:0,...extra});
const workspace=(name,extra={})=>({name,path:'/projects/'+name,description:'',access_mode:'approval',...extra});
test('running sessions outrank pins and manual positions',()=>{
 const source=[session('pinned',{pinned:true,position:1}),session('recent',{last_active_at:10000}),session('working',{running_tasks:1,last_active_at:1}),session('manual',{position:2})];
 assert.deepEqual(sortSessions(source).map(x=>x.id),['working','pinned','manual','recent']);assert.equal(source[0].id,'pinned');
});
test('tool calls and queued operations count as actual work, active session alone does not',()=>{
 assert.equal(isWorking(session('idle')),false);assert.equal(isWorking(session('tool',{running_calls:1})),true);assert.equal(isWorking(session('queue',{running_operations:1})),true);
 assert.equal(workingLabel(session('tool',{running_calls:2})),'工具调用中');
});
test('custom ordering is restored after a command finishes',()=>{
 const a=session('a',{position:2,running_tasks:1}),b=session('b',{position:1});
 assert.deepEqual(sortSessions([b,a]).map(x=>x.id),['a','b']);a.running_tasks=0;assert.deepEqual(sortSessions([a,b]).map(x=>x.id),['b','a']);
});
test('workspace pins precede custom ordering without automatically changing project',()=>{
 assert.deepEqual(sortWorkspaces([workspace('a',{position:1}),workspace('b',{pinned:true}),workspace('c',{position:2})]).map(x=>x.name),['b','a','c']);
});
test('dragging cannot rebind a session to another workspace or priority tier',()=>{
 const a=sessionTarget(session('a'));assert.equal(canDrop(a,sessionTarget(session('b'))),true);
 for(const other of [session('a'),session('b',{workspace:'beta'}),session('b',{pinned:true}),session('b',{running_tasks:1})])assert.equal(canDrop(a,sessionTarget(other)),false);
 assert.equal(canDrop(a,workspaceTarget(workspace('a'))),false);
});
test('workspaces can be reordered even when one contains work; session running priority remains local',()=>{
 assert.equal(canDrop(workspaceTarget(workspace('a',{working_sessions:1})),workspaceTarget(workspace('b'))),true);
});
