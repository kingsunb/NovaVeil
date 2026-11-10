/**
 * Enforce an Agent Note for non-trivial pull request changes.
 * Env: AGENT_NOTE_PR_BASE_SHA, AGENT_NOTE_PR_HEAD_SHA,
 *      AGENT_NOTE_PR_LABELS (JSON array), AGENT_NOTE_SKIP_LABELS (comma list).
 * Run: npx tsx scripts/agent-notes/verify-pr-note.ts
 */
import { spawnSync } from "node:child_process";

const NOTE_ROOT = ".agents/notes/";
const NOTE_EXCLUDED = new Set([
  ".agents/notes/README.md",
  ".agents/notes/AGENTS.md",
]);

const CODE_PREFIXES = ["internal/", "cmd/", "web-next/", "scripts/", ".github/"];
const CONTRACT_DOC_PREFIXES = ["docs/modules/", "docs/脱敏开发/"];
const CONTRACT_DOC_FILES = new Set([
  "docs/SECURE_DEPLOYMENT.md",
  "docs/STANDALONE_DEPLOYMENT.md",
  "docs/BACKUP_RESTORE.md",
  "docs/DEVELOPMENT_routing.md",
  "docs/CHANNEL_PASSTHROUGH.md",
]);
const CONTRACT_ROOT_FILES = new Set([
  "AGENTS.md",
  "package.json",
  "pnpm-lock.yaml",
  "go.mod",
  "go.sum",
  ".gitignore",
  "lefthook.yml",
  "Dockerfile",
  "docker-compose.yml",
  "docker-compose.local.yml",
]);
const LOCKFILE_ONLY = new Set([
  "go.sum",
  "pnpm-lock.yaml",
  "web-next/pnpm-lock.yaml",
]);

const DEFAULT_SKIP_LABELS = "skip-agent-note,dependencies";

interface Change {
  status: string;
  path: string;
}

function runGit(args: string[]): string {
  const result = spawnSync("git", args, { encoding: "utf8" });
  if (result.status !== 0) {
    const detail = (result.stderr || result.stdout || "").trim();
    throw new Error(`git ${args.join(" ")} failed${detail ? `: ${detail}` : ""}`);
  }
  return result.stdout;
}

function commitExists(ref: string): boolean {
  const result = spawnSync("git", ["cat-file", "-e", `${ref}^{commit}`], {
    encoding: "utf8",
  });
  return result.status === 0;
}

function parseLabels(raw: string | undefined): string[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      return parsed.filter((item): item is string => typeof item === "string");
    }
  } catch {
    // Fall back to a comma-separated list for local runs.
  }
  return raw.split(",").map((item) => item.trim()).filter(Boolean);
}

function isNotePath(path: string): boolean {
  return (
    path.startsWith(NOTE_ROOT) &&
    path.endsWith(".md") &&
    !path.startsWith(`${NOTE_ROOT}archived/`) &&
    !NOTE_EXCLUDED.has(path)
  );
}

function isNonTrivial(path: string): boolean {
  if (LOCKFILE_ONLY.has(path)) return false;
  if (CODE_PREFIXES.some((prefix) => path.startsWith(prefix))) return true;
  if (CONTRACT_DOC_PREFIXES.some((prefix) => path.startsWith(prefix))) return true;
  if (CONTRACT_DOC_FILES.has(path)) return true;
  if (CONTRACT_ROOT_FILES.has(path)) return true;
  return false;
}

function parseChanges(diff: string): Change[] {
  const tokens = diff.split("\0");
  const changes: Change[] = [];
  for (let i = 0; i + 1 < tokens.length; i += 2) {
    const status = tokens[i]?.trim();
    const path = tokens[i + 1];
    if (status && path) changes.push({ status, path });
  }
  return changes;
}

const base = process.env.AGENT_NOTE_PR_BASE_SHA?.trim();
const head = process.env.AGENT_NOTE_PR_HEAD_SHA?.trim();
if (!base || !head) {
  console.error(
    "verify-pr-note: set AGENT_NOTE_PR_BASE_SHA and AGENT_NOTE_PR_HEAD_SHA (CI sets them from the pull_request event).",
  );
  process.exit(2);
}
if (!commitExists(base) || !commitExists(head)) {
  console.error(
    `verify-pr-note: cannot resolve base (${base}) or head (${head}); fetch the full PR history (actions/checkout fetch-depth: 0).`,
  );
  process.exit(2);
}

let changes: Change[];
try {
  const mergeBase = runGit(["merge-base", base, head]).trim();
  changes = parseChanges(
    runGit(["diff", "--name-status", "--no-renames", "-z", `${mergeBase}..${head}`]),
  );
} catch (error) {
  console.error(`verify-pr-note: ${(error as Error).message}`);
  process.exit(2);
}

const skipLabels = new Set(
  (process.env.AGENT_NOTE_SKIP_LABELS || DEFAULT_SKIP_LABELS)
    .split(",")
    .map((label) => label.trim())
    .filter(Boolean),
);
const labels = parseLabels(process.env.AGENT_NOTE_PR_LABELS);
const matchedLabels = labels.filter((label) => skipLabels.has(label));

const touched = changes.filter((change) => isNonTrivial(change.path));
const touchedPaths = [...new Set(touched.map((change) => change.path))];

if (touched.length === 0) {
  console.log("ok: no non-trivial paths changed; Agent Note not required");
  process.exit(0);
}

if (matchedLabels.length > 0) {
  console.log(
    `ok: exempt by label ${matchedLabels.join(", ")}; changed non-trivial files: ${touchedPaths.length}`,
  );
  process.exit(0);
}

const noteChanged = changes.some(
  (change) => change.status !== "D" && isNotePath(change.path),
);

if (noteChanged) {
  console.log("ok: non-trivial change carries an active Agent Note");
  process.exit(0);
}

console.error("verify-pr-note: this PR changes non-trivial files but adds/updates no Agent Note.");
console.error("");
console.error("Non-trivial files:");
for (const path of touchedPaths.slice(0, 20)) console.error(`  ${path}`);
if (touchedPaths.length > 20) {
  console.error(`  ... and ${touchedPaths.length - 20} more`);
}
console.error("");
console.error("Fix one of:");
console.error(
  "  1. Add/update a note under .agents/notes/{proposed,implemented,rejected}/<class>/yyyy-mm-dd-topic.md.",
);
console.error(
  "  2. For a purely mechanical change, add the `skip-agent-note` label (or `dependencies` for dependency bumps).",
);
console.error("Rules: .agents/notes/README.md#何时写");
process.exit(1);
