#!/usr/bin/env node
// Explicit allowlist: never package credentials, local config, evals or app-building skills.
import {mkdir,cp,readFile,writeFile} from 'node:fs/promises';
import {resolve,dirname,join} from 'node:path';
import {fileURLToPath} from 'node:url';
const [output,...rest]=process.argv.slice(2);
if(!output || output==='--help') { console.log('Usage: node scripts/package-swarm-codex.mjs NEW_OUTPUT_DIRECTORY [--skills-only]\nDefault: local Codex skills and MCP adapter. --skills-only: portable Swarm Setup skills package for a separately configured ChatGPT connection, without an auto-started MCP server. Both create a local marketplace. Zip the contents of plugins/swarm for sharing.');process.exit(0); }
const skillsOnly=rest.length===1 && rest[0]==='--skills-only';
if(rest.length && !skillsOnly) throw Error('Expected a new output directory and optional --skills-only.');
const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const out=resolve(output), source=join(root,'plugins/swarm'), target=join(out,'plugins/swarm');
await mkdir(out); // Fail if present: never overwrite another installation.
await mkdir(target,{recursive:true});
const files=['bin','skills/setup','skills/using-swarm','README.md'];
if(!skillsOnly) files.push('.codex-plugin','.mcp-codex.json');
for(const file of files) {
  await mkdir(dirname(join(target,file)),{recursive:true});
  await cp(join(source,file),join(target,file),{recursive:true});
}
const manifest=JSON.parse(await readFile(join(source,'.codex-plugin/plugin.json'),'utf8'));
if(skillsOnly) {
  manifest.name='swarm-setup';
  const portable={
    $schema:'https://agent-plugins.org/schemas/1.0.0/plugin.schema.json',
    name:manifest.name,version:manifest.version,
    description:'Set up your Swarm and connect it to ChatGPT, then delegate through your own MCP connection.',
    author:manifest.author,homepage:manifest.homepage,
    extensions:{'com.openai':{
      onboardingSkill:'./skills/setup/SKILL.md',
      interface:{...manifest.interface,displayName:'Swarm Setup',shortDescription:'Connect your own Swarm',
        longDescription:'Setup and delegation skills for your own Swarm. Includes the verified ChatGPT private-tunnel workflow. Requires authorized execution on the selected host for deployment and a separate per-user MCP connection for tools. This package does not start a local MCP process or contain a daemon, tunnel ID or credentials.',
        defaultPrompt:['Set up my Swarm and connect it privately to my ChatGPT dot.','Use my connected Swarm to delegate a task.']}
    }}
  };
  await writeFile(join(target,'plugin.json'),JSON.stringify(portable,null,2)+'\n');
}
await mkdir(join(out,'.agents/plugins'),{recursive:true});
await writeFile(join(out,'.agents/plugins/marketplace.json'),JSON.stringify({name:skillsOnly?'swarm-setup':'swarm-local',plugins:[{name:manifest.name,source:{source:'local',path:'./plugins/swarm'},policy:{installation:'AVAILABLE',authentication:'ON_INSTALL'},category:'Developer Tools'}]},null,2)+'\n');
console.log('Packaged '+manifest.name+' '+manifest.version+' at '+out);
