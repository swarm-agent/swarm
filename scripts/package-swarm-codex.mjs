#!/usr/bin/env node
// Explicit allowlist: never package credentials, local config, evals or app-building skills.
import {mkdir,cp,readFile,writeFile} from 'node:fs/promises';
import {resolve,dirname,join} from 'node:path';
import {fileURLToPath} from 'node:url';
const [output,...rest]=process.argv.slice(2);
if(!output || output==='--help') { console.log('Usage: node scripts/package-swarm-codex.mjs NEW_OUTPUT_DIRECTORY\nCreates an MCP-only plugin folder and a local Codex marketplace. Zip the contents of plugins/swarm for sharing.');process.exit(0); }
if(rest.length) throw Error('Expected one new output directory.');
const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const out=resolve(output), source=join(root,'plugins/swarm'), target=join(out,'plugins/swarm');
await mkdir(out); // Fail if present: never overwrite another installation.
await mkdir(target,{recursive:true});
for(const file of ['.codex-plugin','.mcp-codex.json','bin','skills/setup','skills/using-swarm','README.md']) {
  await mkdir(dirname(join(target,file)),{recursive:true});
  await cp(join(source,file),join(target,file),{recursive:true});
}
const manifest=JSON.parse(await readFile(join(target,'.codex-plugin/plugin.json'),'utf8'));
await mkdir(join(out,'.agents/plugins'),{recursive:true});
await writeFile(join(out,'.agents/plugins/marketplace.json'),JSON.stringify({name:'swarm-local',plugins:[{name:manifest.name,source:{source:'local',path:'./plugins/swarm'},policy:{installation:'AVAILABLE',authentication:'ON_INSTALL'},category:'Developer Tools'}]},null,2)+'\n');
console.log('Packaged '+manifest.name+' '+manifest.version+' at '+out);
