// Requirement: connection metadata cannot smuggle credentials into URLs, select
// ambiguous transports, or read someone else's/world-readable secret file.
// Authority: validateConnection/readToken. Pure configuration and real temporary
// file checks are the narrowest layer; these do not simulate agents or inference.
import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,chmod,symlink,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {validateConnection,readToken} from '../bin/lib/connection.mjs';

test('connection choices reject credential URLs and malformed targets',()=>{
  assert.deepEqual(validateConnection({mode:'http',url:'https://swarm.example/mcp'}),{mode:'http',url:'https://swarm.example/mcp'});
  for(const url of ['http://swarm.example/mcp','https://user:secret@swarm.example/mcp','https://swarm.example/mcp?key=secret','https://swarm.example/mcp#secret','https://swarm.example/']) assert.throws(()=>validateConnection({mode:'http',url}));
  assert.throws(()=>validateConnection({mode:'http',url:'https://swarm.example/mcp',tokenFile:'relative'}));
  assert.throws(()=>validateConnection({mode:'docker',container:'--privileged'}));
  assert.throws(()=>validateConnection({mode:'docker',container:'name; shell'}));
  assert.throws(()=>validateConnection({mode:'unknown'}));
  assert.deepEqual(validateConnection({mode:'docker',container:'swarm-headless'}),{mode:'docker',container:'swarm-headless'});
});

test('key file access rejects broad permissions and symlinks',async()=>{
  const dir=await mkdtemp(join(tmpdir(),'swarm-mcp-config-'));
  try {
    const key=join(dir,'key');await writeFile(key,'test-only-not-a-credential',{mode:0o600});
    assert.equal(await readToken(key),'test-only-not-a-credential');
    if(process.platform!=='win32') {
      await chmod(key,0o644); await assert.rejects(readToken(key)); await chmod(key,0o600);
      await symlink(key,join(dir,'link')); await assert.rejects(readToken(join(dir,'link')));
    }
    await writeFile(key,'two values'); await assert.rejects(readToken(key));
    await writeFile(key,'x'.repeat(8193)); await assert.rejects(readToken(key));
  } finally {await rm(dir,{recursive:true,force:true});}
});
