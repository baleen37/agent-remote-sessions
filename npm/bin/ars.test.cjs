const assert = require("node:assert/strict");
const test = require("node:test");

const { binaryName, run } = require("./ars.js");

test("binaryName maps only supported targets", () => {
  assert.equal(binaryName("darwin", "arm64"), "ars-darwin-arm64");
  assert.equal(binaryName("linux", "x64"), "ars-linux-amd64");
  assert.equal(binaryName("linux", "arm64"), "ars-linux-arm64");
  assert.equal(binaryName("darwin", "x64"), null);
  assert.equal(binaryName("win32", "x64"), null);
});

test("run inherits stdio and returns the native exit code", () => {
  let call;
  const code = run({
    platform: "linux",
    arch: "x64",
    dirname: "/package/bin",
    args: ["list", "--json"],
    spawnSync(binary, args, options) {
      call = { binary, args, options };
      return { status: 23, signal: null };
    },
    stderr: { write() { throw new Error("unexpected stderr"); } },
    kill() { throw new Error("unexpected signal"); },
    pid: 100,
  });

  assert.equal(code, 23);
  assert.deepEqual(call, {
    binary: "/package/vendor/ars-linux-amd64",
    args: ["list", "--json"],
    options: { stdio: "inherit" },
  });
});

test("run mirrors a terminating signal", () => {
  let killed;
  const code = run({
    platform: "darwin",
    arch: "arm64",
    dirname: "/package/bin",
    args: [],
    spawnSync() { return { status: null, signal: "SIGTERM" }; },
    stderr: { write() {} },
    kill(pid, signal) { killed = { pid, signal }; },
    pid: 101,
  });

  assert.equal(code, 1);
  assert.deepEqual(killed, { pid: 101, signal: "SIGTERM" });
});

test("run rejects unsupported platforms before spawning", () => {
  let stderr = "";
  const code = run({
    platform: "win32",
    arch: "x64",
    dirname: "/package/bin",
    args: [],
    spawnSync() { throw new Error("must not spawn"); },
    stderr: { write(value) { stderr += value; } },
    kill() {},
    pid: 102,
  });

  assert.equal(code, 1);
  assert.match(stderr, /unsupported platform win32\/x64/);
  assert.match(stderr, /darwin\/arm64, linux\/amd64, linux\/arm64/);
});

test("run reports native process start failures", () => {
  let stderr = "";
  const code = run({
    platform: "linux",
    arch: "arm64",
    dirname: "/package/bin",
    args: [],
    spawnSync() { return { error: new Error("permission denied") }; },
    stderr: { write(value) { stderr += value; } },
    kill() {},
    pid: 103,
  });

  assert.equal(code, 1);
  assert.match(stderr, /start native binary: permission denied/);
});
