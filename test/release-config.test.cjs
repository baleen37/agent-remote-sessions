const assert = require("node:assert/strict");
const test = require("node:test");

const config = require("../release.config.cjs");

test("release config is main-only and publishes generated assets", () => {
  assert.deepEqual(config.branches, ["main"]);
  assert.equal(config.tagFormat, "v${version}");
  assert.deepEqual(
    config.plugins.map((plugin) => (Array.isArray(plugin) ? plugin[0] : plugin)),
    [
      "@semantic-release/commit-analyzer",
      "@semantic-release/release-notes-generator",
      "@semantic-release/exec",
      "@semantic-release/npm",
      "@semantic-release/github",
    ],
  );
  assert.equal(
    config.plugins[2][1].prepareCmd,
    "go run ./cmd/ars-build --release ${nextRelease.version}",
  );
  assert.equal(config.plugins[3][1].pkgRoot, "dist/npm");
  assert.deepEqual(
    config.plugins[4][1].assets.map((asset) => asset.path),
    [
      "dist/ars_*_darwin_arm64.tar.gz",
      "dist/ars_*_linux_amd64.tar.gz",
      "dist/ars_*_linux_arm64.tar.gz",
      "dist/SHA256SUMS",
    ],
  );
  assert.equal(config.plugins[4][1].successComment, false);
  assert.equal(config.plugins[4][1].failComment, false);
  assert.equal(config.plugins[4][1].releasedLabels, false);
});

test("commit analyzer maps the accepted release contract", async () => {
  const { analyzeCommits } = await import("@semantic-release/commit-analyzer");
  const analyzer = config.plugins[0][1];
  const logger = { log() {} };
  const classify = (message) => analyzeCommits(analyzer, {
    commits: [{ message }],
    logger,
  });

  assert.equal(await classify("feat: add picker"), "minor");
  assert.equal(await classify("fix: preserve exit code"), "patch");
  assert.equal(await classify("perf: reduce allocations"), "patch");
  assert.equal(
    await classify("feat: replace protocol\n\nBREAKING CHANGE: schema changed"),
    "major",
  );
  assert.equal(await classify("docs: explain install"), null);
  assert.equal(await classify("chore: update metadata"), null);
  assert.equal(await classify("test: cover launcher"), null);
});
