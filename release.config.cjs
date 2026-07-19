module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  plugins: [
    ["@semantic-release/commit-analyzer", {
      releaseRules: [
        { type: "docs", release: false },
        { type: "chore", release: false },
        { type: "test", release: false },
      ],
    }],
    "@semantic-release/release-notes-generator",
    ["@semantic-release/exec", {
      prepareCmd: "go run ./cmd/ars-build --release ${nextRelease.version}",
    }],
    ["@semantic-release/npm", {
      pkgRoot: "dist/npm",
    }],
    ["@semantic-release/github", {
      assets: [
        { path: "dist/ars_*_darwin_arm64.tar.gz", label: "ars darwin/arm64" },
        { path: "dist/ars_*_linux_amd64.tar.gz", label: "ars linux/amd64" },
        { path: "dist/ars_*_linux_arm64.tar.gz", label: "ars linux/arm64" },
        { path: "dist/SHA256SUMS", label: "SHA256 checksums" },
      ],
      successComment: false,
      failComment: false,
      releasedLabels: false,
    }],
  ],
};
