const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

const workflow = fs.readFileSync(".github/workflows/ci.yml", "utf8");

test("release waits for verification and is restricted to main pushes", () => {
  assert.match(workflow, /release:\n[\s\S]*needs: verify/);
  assert.match(workflow, /github\.event_name == 'push'/);
  assert.match(workflow, /github\.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /group: release-main/);
  assert.match(workflow, /cancel-in-progress: false/);
});

test("release alone can write contents and request an OIDC token", () => {
  assert.match(
    workflow,
    /release:[\s\S]*permissions:[\s\S]*contents: write[\s\S]*id-token: write/,
  );
  assert.match(workflow, /fetch-depth: 0/);
  assert.match(workflow, /node-version: 24\.15\.0/);
  assert.match(workflow, /npm run release/);
  assert.doesNotMatch(workflow, /NPM_TOKEN|NODE_AUTH_TOKEN/);
});
