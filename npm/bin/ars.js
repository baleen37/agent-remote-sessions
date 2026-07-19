#!/usr/bin/env node

const path = require("node:path");
const { spawnSync } = require("node:child_process");

const binaries = new Map([
  ["darwin/arm64", "ars-darwin-arm64"],
  ["linux/x64", "ars-linux-amd64"],
  ["linux/arm64", "ars-linux-arm64"],
]);

function binaryName(platform, arch) {
  return binaries.get(`${platform}/${arch}`) ?? null;
}

function run(runtime = {
  platform: process.platform,
  arch: process.arch,
  dirname: __dirname,
  args: process.argv.slice(2),
  spawnSync,
  stderr: process.stderr,
  kill: process.kill.bind(process),
  pid: process.pid,
}) {
  const name = binaryName(runtime.platform, runtime.arch);
  if (name === null) {
    runtime.stderr.write(
      `ars: unsupported platform ${runtime.platform}/${runtime.arch}; ` +
      "supported: darwin/arm64, linux/amd64, linux/arm64\n",
    );
    return 1;
  }

  const binary = path.join(runtime.dirname, "..", "vendor", name);
  const result = runtime.spawnSync(binary, runtime.args, { stdio: "inherit" });
  if (result.error) {
    runtime.stderr.write(`ars: start native binary: ${result.error.message}\n`);
    return 1;
  }
  if (result.signal) {
    runtime.kill(runtime.pid, result.signal);
    return 1;
  }
  return result.status ?? 1;
}

if (require.main === module) {
  process.exitCode = run();
}

module.exports = { binaryName, run };
