module.exports = {
  branches: ["master", { name: "dev", prerelease: true }],
  plugins: [
    "@semantic-release/commit-analyzer",
    "@semantic-release/release-notes-generator",
    [
      "@semantic-release/changelog",
      {
        changelogFile: "CHANGELOG.md",
      },
    ],
    [
      "@semantic-release/exec",
      {
        prepareCmd:
          "node ./scripts/update-readme-version.js ${nextRelease.version}",
      },
    ],
    [
      "@codedependant/semantic-release-docker",
      {
        dockerRegistry: "ghcr.io",
        dockerImage: "ghcr.io/alessandrozanatta/external-dns-porkbun-webhook",
        dockerFile: "Dockerfile",
        dockerTags: [
          "{{version}}",
          "{{major}}-{{channel}}",
          "{{major}}.{{minor}}-{{channel}}",
          "{{channel}}",
        ],
      },
    ],
    [
      "@semantic-release/git",
      {
        assets: ["CHANGELOG.md", "README.md"],
        message:
          "chore(release): ${nextRelease.version} [skip ci]\n\n${nextRelease.notes}",
      },
    ],
    ["@semantic-release/github"],
  ],
};
