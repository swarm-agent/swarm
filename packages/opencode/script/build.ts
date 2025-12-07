#!/usr/bin/env bun

import solidPlugin from "../node_modules/@opentui/solid/scripts/solid-plugin"
import path from "path"
import fs from "fs"
import { $ } from "bun"
import { fileURLToPath } from "url"

process.env.PATH = "/usr/bin:/bin:" + (process.env.PATH || "")

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const dir = path.resolve(__dirname, "..")

process.chdir(dir)

import pkg from "../package.json"
import { Script } from "@opencode-ai/script"

const singleFlag = process.argv.includes("--single")

const allTargets: {
  os: string
  arch: "arm64" | "x64"
  abi?: "musl"
  avx2?: false
}[] = [
  {
    os: "linux",
    arch: "arm64",
  },
  {
    os: "linux",
    arch: "x64",
  },
  {
    os: "linux",
    arch: "x64",
    avx2: false,
  },
  {
    os: "linux",
    arch: "arm64",
    abi: "musl",
  },
  {
    os: "linux",
    arch: "x64",
    abi: "musl",
  },
  {
    os: "linux",
    arch: "x64",
    abi: "musl",
    avx2: false,
  },
  {
    os: "darwin",
    arch: "arm64",
  },
  {
    os: "darwin",
    arch: "x64",
  },
  {
    os: "darwin",
    arch: "x64",
    avx2: false,
  },
  {
    os: "windows",
    arch: "x64",
  },
  {
    os: "windows",
    arch: "x64",
    avx2: false,
  },
]

const targets = singleFlag
  ? allTargets.filter((item) => 
      item.os === process.platform && 
      item.arch === process.arch && 
      item.avx2 !== false &&  // skip baseline variants for single build
      item.abi === undefined  // skip musl variants for single build
    )
  : allTargets

await $`rm -rf dist`

const binaries: Record<string, string> = {}
await $`bun install --os="*" --cpu="*" @opentui/core@${pkg.dependencies["@opentui/core"]}`
await $`bun install --os="*" --cpu="*" @parcel/watcher@${pkg.dependencies["@parcel/watcher"]}`
for (const item of targets) {
  const name = [
    pkg.name,
    item.os,
    item.arch,
    item.avx2 === false ? "baseline" : undefined,
    item.abi === undefined ? undefined : item.abi,
  ]
    .filter(Boolean)
    .join("-")
  console.log(`building ${name}`)
  await $`mkdir -p dist/${name}/bin`

  // Extract platform-specific native modules (our custom explicit path version)
  const opentuiVersion = pkg.dependencies["@opentui/core"]
  const watcherVersion = pkg.dependencies["@parcel/watcher"]
  const opentui = `@opentui/core-${item.os === "windows" ? "win32" : item.os}-${item.arch}${item.avx2 === false ? "-baseline" : ""}`
  await $`mkdir -p ../../node_modules/${opentui}`
  
  // Use npm with a writable cache
  const npmCacheDir = path.join(dir, "../../.npm-cache")
  await $`mkdir -p ${npmCacheDir}`
  
  await Bun.spawn(["/usr/bin/npm", "pack", "--cache", npmCacheDir, `${opentui}@${opentuiVersion}`], {
    cwd: path.join(dir, "../../node_modules"),
    stdio: ["inherit", "inherit", "inherit"],
  }).exited
  const opentuiTgz = `${opentui.replace("@opentui/", "opentui-")}-${opentuiVersion}.tgz`
  await $`cd ../../node_modules && /usr/bin/tar -xf ${opentuiTgz} -C ${opentui} --strip-components=1`

  const watcher = `@parcel/watcher-${item.os === "windows" ? "win32" : item.os}-${item.arch}${item.avx2 === false ? "-baseline" : ""}${item.os === "linux" && !item.abi ? "-glibc" : ""}`
  await $`mkdir -p ../../node_modules/${watcher}`
  await Bun.spawn(["/usr/bin/npm", "pack", "--cache", npmCacheDir, watcher], {
    cwd: path.join(dir, "../../node_modules"),
    stdio: ["inherit", "pipe", "pipe"],
  }).exited
  const watcherTgz = `${watcher.replace("@parcel/", "parcel-")}-${watcherVersion}.tgz`
  await $`cd ../../node_modules && /usr/bin/tar -xf ${watcherTgz} -C ${watcher} --strip-components=1`

  const parserWorker = fs.realpathSync(path.resolve(dir, "./node_modules/@opentui/core/parser.worker.js"))
  const workerPath = "./src/cli/cmd/tui/worker.ts"

  await Bun.build({
    conditions: ["browser"],
    tsconfig: "./tsconfig.json",
    plugins: [solidPlugin],
    sourcemap: "external",
    compile: {
      target: name.replace(pkg.name, "bun") as any,
      outfile: `dist/${name}/bin/swarm`,
      execArgv: [`--user-agent=swarm/${Script.version}`, `--env-file=""`, `--`],
      windows: {},
    },
    entrypoints: ["./src/index.ts", parserWorker, workerPath],
    define: {
      OPENCODE_VERSION: `'${Script.version}'`,
      OTUI_TREE_SITTER_WORKER_PATH: "/$bunfs/root/" + path.relative(dir, parserWorker).replaceAll("\\", "/"),
      OPENCODE_WORKER_PATH: workerPath,
      OPENCODE_CHANNEL: `'${Script.channel}'`,
    },
  })

  await $`rm -rf ./dist/${name}/bin/tui`
  await Bun.file(`dist/${name}/package.json`).write(
    JSON.stringify(
      {
        name,
        version: Script.version,
        os: [item.os === "windows" ? "win32" : item.os],
        cpu: [item.arch],
      },
      null,
      2,
    ),
  )
  binaries[name] = Script.version
}

export { binaries }
