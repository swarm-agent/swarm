import test from 'node:test'
import assert from 'node:assert/strict'
import {selectTaskPlanDocument} from './orchestrate-plan-authority'
test('new task definition wins over stale rejected session plan',()=>{
 const current={id:'p',status:'pending_approval'}
 const task={planDocument:current,planBinding:{planId:'p',definitionRevision:3}}
 assert.equal(selectTaskPlanDocument(task,{id:'p',version:2,document:{id:'p',status:'rejected'}}),current)
 assert.equal(selectTaskPlanDocument(task,{id:'different',version:9,document:{}}),current)
 assert.equal(selectTaskPlanDocument(task,{id:'p',document:{status:'rejected'}}),current)
 const executing={id:'p',status:'in_progress'}
 assert.equal(selectTaskPlanDocument(task,{id:'p',version:4,document:executing}),executing)
})
