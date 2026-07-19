const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

const workflow = fs.readFileSync(".github/workflows/ci.yml", "utf8");
const releaseJob = workflow.slice(workflow.indexOf("\n  release:"));

test("release waits for verification and is restricted to main pushes", () => {
  assert.notEqual(releaseJob, "");
  assert.match(releaseJob, /needs: verify/);
  assert.match(releaseJob, /github\.event_name == 'push'/);
  assert.match(releaseJob, /github\.ref == 'refs\/heads\/main'/);
  assert.match(releaseJob, /group: release-main/);
  assert.match(releaseJob, /cancel-in-progress: false/);
});

test("release alone can write contents and request an OIDC token", () => {
  assert.match(releaseJob, /permissions:[\s\S]*contents: write[\s\S]*id-token: write/);
  assert.match(releaseJob, /fetch-depth: 0/);
  assert.match(releaseJob, /node-version: 24\.15\.0/);
  assert.match(releaseJob, /npm run release/);
  assert.doesNotMatch(releaseJob, /NPM_TOKEN|NODE_AUTH_TOKEN/);
});

test("release pins third-party actions by full commit SHA", () => {
  assert.match(releaseJob, /actions\/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5/);
  assert.match(releaseJob, /actions\/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff/);
  assert.match(releaseJob, /actions\/setup-node@820762786026740c76f36085b0efc47a31fe5020/);
  assert.doesNotMatch(releaseJob, /uses: actions\/[^@]+@v[0-9]/);
});
