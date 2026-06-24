#!/usr/bin/env node
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'

const root = resolve(new URL('..', import.meta.url).pathname)
const schemaPath = resolve(root, 'contracts/session-connection.v1.schema.json')
const goOutPath = resolve(root, 'swarmd/internal/api/sessions_v3_connection_contract.generated.go')
const tsOutPath = resolve(root, 'web/src/features/desktop/session-connection/contract.generated.ts')
const schema = JSON.parse(readFileSync(schemaPath, 'utf8'))
const defs = schema.$defs ?? {}

const requiredDefs = [
  'RunPhase',
  'SessionErrorAction',
  'SessionConnectionError',
  'SessionConnectRequest',
  'SessionCurrentRun',
  'SessionSnapshot',
  'SessionConnectionInfo',
  'SessionConnectResponse',
  'RunPhaseReason',
  'SessionReadyFrame',
  'SessionEventFrame',
  'RunPhaseFrame',
  'SessionReconnectRequiredFrame',
  'SessionStreamFrame',
]
for (const name of requiredDefs) {
  if (!defs[name]) throw new Error(`schema is missing $defs/${name}`)
}

function refName(ref) {
  const prefix = '#/$defs/'
  if (typeof ref !== 'string' || !ref.startsWith(prefix)) throw new Error(`unsupported ref ${ref}`)
  return ref.slice(prefix.length)
}

function requiredSet(def) {
  return new Set(def.required ?? [])
}

function propGoType(prop) {
  if (prop.$ref) return refName(prop.$ref)
  if (prop['x-go-type']) return prop['x-go-type']
  if (prop.anyOf) {
    const nonNull = prop.anyOf.filter((item) => item.type !== 'null')
    if (nonNull.length !== 1) throw new Error(`unsupported anyOf ${JSON.stringify(prop)}`)
    return `*${propGoType(nonNull[0])}`
  }
  if (prop.oneOf) throw new Error('nested oneOf is not supported')
  switch (prop.type) {
    case 'string':
      return 'string'
    case 'boolean':
      return 'bool'
    case 'integer':
      return 'uint64'
    case 'object':
      return 'json.RawMessage'
    case 'array':
      return `[]${propGoType(prop.items)}`
    default:
      throw new Error(`unsupported Go schema type ${JSON.stringify(prop)}`)
  }
}

function propTSType(prop) {
  if (prop.$ref) return refName(prop.$ref)
  if (prop['x-ts-type']) return prop['x-ts-type']
  if (prop.const !== undefined) return JSON.stringify(prop.const)
  if (prop.enum) return prop.enum.map((value) => JSON.stringify(value)).join(' | ')
  if (prop.anyOf) return prop.anyOf.map(propTSType).join(' | ')
  if (prop.oneOf) return prop.oneOf.map(propTSType).join(' | ')
  switch (prop.type) {
    case 'string':
      return 'string'
    case 'boolean':
      return 'boolean'
    case 'integer':
      return 'number'
    case 'null':
      return 'null'
    case 'object':
      return 'unknown'
    case 'array':
      return `${propTSType(prop.items)}[]`
    default:
      throw new Error(`unsupported TS schema type ${JSON.stringify(prop)}`)
  }
}

function goStruct(name, def) {
  const required = requiredSet(def)
  const props = def.properties ?? {}
  const fields = Object.keys(props).map((jsonName) => {
    const fieldName = jsonName.split('_').map((part) => part ? part[0].toUpperCase() + part.slice(1) : '').join('')
    const tagSuffix = required.has(jsonName) ? '' : ',omitempty'
    return `\t${fieldName} ${propGoType(props[jsonName])} \`json:"${jsonName}${tagSuffix}"\``
  })
  return `type ${name} struct {\n${fields.join('\n')}\n}\n`
}

function goEnum(name, def) {
  const constants = def.enum.map((value) => {
    const constName = name + value.split(/[^a-zA-Z0-9]+/).filter(Boolean).map((part) => part[0].toUpperCase() + part.slice(1)).join('')
    return `\t${constName} ${name} = "${value}"`
  })
  return `type ${name} string\n\nconst (\n${constants.join('\n')}\n)\n`
}

function tsInterface(name, def) {
  const required = requiredSet(def)
  const props = def.properties ?? {}
  const fields = Object.keys(props).map((jsonName) => {
    const optional = required.has(jsonName) ? '' : '?'
    return `  ${jsonName}${optional}: ${propTSType(props[jsonName])}`
  })
  return `export interface ${name} {\n${fields.join('\n')}\n}\n`
}

function tsType(name, def) {
  if (def.enum) return `export type ${name} = ${def.enum.map((value) => JSON.stringify(value)).join(' | ')}\n`
  if (def.oneOf) return `export type ${name} = ${def.oneOf.map((item) => propTSType(item)).join(' | ')}\n`
  return tsInterface(name, def)
}

function writeGo() {
  const goDefs = [
    goEnum('RunPhase', defs.RunPhase),
    goStruct('SessionErrorAction', defs.SessionErrorAction),
    goStruct('SessionConnectionError', defs.SessionConnectionError),
    goStruct('SessionConnectRequest', defs.SessionConnectRequest),
    goStruct('RunPhaseReason', defs.RunPhaseReason),
    goStruct('SessionCurrentRun', defs.SessionCurrentRun),
    goStruct('SessionSnapshot', defs.SessionSnapshot),
    goStruct('SessionConnectionInfo', defs.SessionConnectionInfo),
    goStruct('SessionConnectResponse', defs.SessionConnectResponse),
    goStruct('SessionReadyFrame', defs.SessionReadyFrame),
    goStruct('SessionEventFrame', defs.SessionEventFrame),
    goStruct('RunPhaseFrame', defs.RunPhaseFrame),
    goEnum('SessionReconnectRequiredReason', defs.SessionReconnectRequiredFrame.properties.reason),
    goStruct('SessionReconnectRequiredFrame', defs.SessionReconnectRequiredFrame),
    `type SessionStreamFrame interface {\n\tsessionStreamFrame()\n}\n\nfunc (SessionReadyFrame) sessionStreamFrame() {}\nfunc (SessionEventFrame) sessionStreamFrame() {}\nfunc (RunPhaseFrame) sessionStreamFrame() {}\nfunc (SessionReconnectRequiredFrame) sessionStreamFrame() {}\n`,
  ]
  const out = `// Code generated by scripts/generate-session-connection-contract.mjs from contracts/session-connection.v1.schema.json; DO NOT EDIT.\n\npackage api\n\nimport "encoding/json"\n\nconst SessionConnectionContractVersion = 1\nconst SessionConnectionProtocol = "swarm.session-stream.v1"\nconst SessionConnectionDefaultReadyTimeoutMS = 10000\n\n${goDefs.join('\n')}`
  mkdirSync(dirname(goOutPath), { recursive: true })
  writeFileSync(goOutPath, out)
}

function writeTS() {
  const tsDefs = [
    tsType('RunPhase', defs.RunPhase),
    tsType('SessionErrorAction', defs.SessionErrorAction),
    tsType('SessionConnectionError', defs.SessionConnectionError),
    tsType('SessionConnectRequest', defs.SessionConnectRequest),
    tsType('RunPhaseReason', defs.RunPhaseReason),
    tsType('SessionCurrentRun', defs.SessionCurrentRun),
    tsType('SessionSnapshot', defs.SessionSnapshot),
    tsType('SessionConnectionInfo', defs.SessionConnectionInfo),
    tsType('SessionConnectResponse', defs.SessionConnectResponse),
    tsType('SessionReadyFrame', defs.SessionReadyFrame),
    tsType('SessionEventFrame', defs.SessionEventFrame),
    tsType('RunPhaseFrame', defs.RunPhaseFrame),
    `export type SessionReconnectRequiredReason = ${defs.SessionReconnectRequiredFrame.properties.reason.enum.map((value) => JSON.stringify(value)).join(' | ')}\n`,
    tsType('SessionReconnectRequiredFrame', defs.SessionReconnectRequiredFrame),
    tsType('SessionStreamFrame', defs.SessionStreamFrame),
  ]
  const out = `// Code generated by scripts/generate-session-connection-contract.mjs from contracts/session-connection.v1.schema.json; DO NOT EDIT.\n\nexport const SESSION_CONNECTION_CONTRACT_VERSION = 1 as const\nexport const SESSION_CONNECTION_PROTOCOL = 'swarm.session-stream.v1' as const\nexport const SESSION_CONNECTION_DEFAULT_READY_TIMEOUT_MS = 10000 as const\n\n${tsDefs.join('\n')}`
  mkdirSync(dirname(tsOutPath), { recursive: true })
  writeFileSync(tsOutPath, out)
}

writeGo()
writeTS()
