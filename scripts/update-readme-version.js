const fs = require("fs");
const path = require("path");

const newVersion = process.argv[2];

if (!newVersion) {
  console.error("Usage: node update-readme-version.js <new-version>");
  process.exit(1);
}

const readmePath = path.join(__dirname, "..", "README.md");
let readmeContent = fs.readFileSync(readmePath, "utf8");

// Regex to find the action with the version in the README
const actionUsageRegex =
  /(ghcr.io\/alessandrozanatta\/external-dns-porkbun-webhook)@\d+\.\d+\.\d+$/g;

// Replace with the new version
const updatedContent = readmeContent.replace(
  actionUsageRegex,
  `$1@v${newVersion}`,
);

if (readmeContent === updatedContent) {
  console.warn(
    "README.md version not updated. Regex might be incorrect or version did not change.",
  );
} else {
  fs.writeFileSync(readmePath, updatedContent, "utf8");
  console.log(`README.md updated to version v${newVersion}`);
}
