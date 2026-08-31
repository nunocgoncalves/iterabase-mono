// Isolation test runner (HOR-381). Sets up two per-session sandboxes on a
// shared mount (distinct UIDs, 0700 roots, 0711 mount root), then launches the
// probe via the REAL setpriv launcher (dist/launcher.js) under each session UID
// and asserts the kernel enforces isolation.
//
// Sequential-isolation (the warm-worker reuse case): probe A runs under
// UID 1000 in sandbox A, then probe B runs under UID 1001 in sandbox B — a
// fresh process with a different UID/HOME/cwd. Each asserts it cannot read the
// other session's files. (The pi-extension-state bleed variant lands when the
// pi child is wired; this proves the process/UID boundary.)
//
// Then the state-bleed regression (probe-state.mjs): runA mutates
// module/global/timer/descriptor state + persists a PVC marker; runB (fresh
// process) proves zero bleed; resumeA (fresh process) proves only PVC state
// restores, not in-memory state.
//
// Runs as ROOT in a privileged Linux test container so it can format and mount
// a fresh real filesystem, then exercise the same trusted-root-supervisor and
// setpriv child boundary production renders. Production retains runtime-default
// capabilities plus explicit SETUID/SETGID on that root supervisor.

import { execSync } from "node:child_process";
import { launchChild } from "./dist/launcher.js";
import { validateSupervisorTLSKey } from "./dist/tls-key.js";

const MOUNT = "/data/sandboxes";
const A = `${MOUNT}/A`;
const B = `${MOUNT}/B`;
const UID_A = 1000;
const GID_A = 1000;
const UID_B = 1001;
const GID_B = 1001;
const BREAK_MODE = process.env.HARNESS_ISOLATION_BREAK ?? "";
const FILESYSTEM = process.env.HARNESS_ISOLATION_FILESYSTEM ?? "";
const IMAGE = `/tmp/harness-isolation-${FILESYSTEM}.img`;
const TLS_KEY = "/etc/harness/tls/tls.key";

if (!["ext4", "xfs"].includes(FILESYSTEM)) {
  throw new Error(`HARNESS_ISOLATION_FILESYSTEM must be ext4 or xfs (got ${FILESYSTEM})`);
}
if (!["", "cross-session-read"].includes(BREAK_MODE)) {
  throw new Error(`unknown HARNESS_ISOLATION_BREAK mode: ${BREAK_MODE}`);
}

function sh(cmd) {
  execSync(cmd, { stdio: "ignore" });
}

function prepareFreshFilesystem() {
  sh(`mkdir -p ${MOUNT}`);
  sh(`truncate -s 512M ${IMAGE}`);
  if (FILESYSTEM === "ext4") {
    sh(`mkfs.ext4 -q -F -L iterabase-ws ${IMAGE}`);
  } else {
    sh(`mkfs.xfs -q -f -L iterabase-ws ${IMAGE}`);
  }
  sh(`mount -o loop ${IMAGE} ${MOUNT}`);
  const actual = execSync(`findmnt -n -o FSTYPE --target ${MOUNT}`, { encoding: "utf8" }).trim();
  if (actual !== FILESYSTEM) throw new Error(`mounted filesystem ${actual}, want ${FILESYSTEM}`);
  const label = execSync(`blkid -s LABEL -o value ${IMAGE}`, { encoding: "utf8" }).trim();
  if (label !== "iterabase-ws") throw new Error(`filesystem label ${label}, want iterabase-ws`);
}

function setup() {
  sh(`find ${MOUNT} -mindepth 1 -delete`);
  for (const [s, uid, gid] of [[A, UID_A, GID_A], [B, UID_B, GID_B]]) {
    for (const p of [s, `${s}/home`, `${s}/tmp`, `${s}/session`, `${s}/workspace`]) {
      sh(`install -d -m 0700 -o ${uid} -g ${gid} ${p}`);
    }
  }
  // A secret in each actual session tree that the other child must not read.
  sh(`echo "A-secret" > ${A}/session/secret.txt && chown ${UID_A}:${GID_A} ${A}/session/secret.txt`);
  sh(`echo "B-secret" > ${B}/session/secret.txt && chown ${UID_B}:${GID_B} ${B}/session/secret.txt`);
  // Pool PVC root: trusted supervisors can access all entries; children may
  // traverse a known path but cannot list or mutate the root.
  sh(`chown 0:0 ${MOUNT} && chmod 0711 ${MOUNT}`);

  sh(`install -d -m 0700 -o 0 -g 0 /etc/harness/tls`);
  sh(`printf private-key-material > ${TLS_KEY} && chown 0:0 ${TLS_KEY} && chmod 0600 ${TLS_KEY}`);
  validateSupervisorTLSKey(TLS_KEY);
  // Independent drift fixtures: validation observes and refuses; it never
  // repairs any bad inode before the real child proof runs.
  sh(`printf bad > /etc/harness/tls/mode.key && chmod 0640 /etc/harness/tls/mode.key`);
  sh(`ln -s ${TLS_KEY} /etc/harness/tls/symlink.key`);
  sh(`mkdir /etc/harness/tls/directory.key`);
  sh(`printf bad > /etc/harness/tls/owner.key && chown ${UID_A}:${GID_A} /etc/harness/tls/owner.key && chmod 0600 /etc/harness/tls/owner.key`);
  for (const bad of ["mode.key", "symlink.key", "directory.key", "owner.key"]) {
    let refused = false;
    try {
      validateSupervisorTLSKey(`/etc/harness/tls/${bad}`);
    } catch {
      refused = true;
    }
    if (!refused) throw new Error(`unsafe TLS key fixture was accepted: ${bad}`);
  }
  console.log(`PASS  supervisor tls.key invariant (${FILESYSTEM})`);
}

function runProbe(label, { uid, gid, sandbox, sibling }) {
  const child = launchChild({
    script: "/app/probe.mjs",
    uid,
    gid,
    sandboxRoot: sandbox,
    workingDir: "home",
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      SANDBOX_ROOT: sandbox,
      SIBLING_ROOT: sibling,
      MOUNT_ROOT: MOUNT,
      TLS_KEY,
      HOME: `${sandbox}/home`,
    },
  });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (d) => (stdout += d));
  child.stderr.on("data", (d) => (stderr += d));
  return new Promise((resolve) => {
    child.on("close", (code) => {
      console.log(`\n=== ${label} (uid=${uid}, sandbox=${sandbox}) ===`);
      process.stdout.write(stdout);
      if (stderr) process.stderr.write(stderr);
      console.log(`exit=${code}`);
      resolve(code === 0);
    });
  });
}

prepareFreshFilesystem();
setup();
if (BREAK_MODE === "cross-session-read") {
  // Sensitivity fixture for HOR-484: deliberately make B traversable/readable.
  // Running this exact image must fail the normal gate rather than masking the
  // cross-session access. The Make negative target expects the non-zero exit.
  sh(`chmod 0755 ${B} ${B}/session && chmod 0644 ${B}/session/secret.txt`);
  console.log("INTENTIONAL BREAK: sandbox B is readable by session A");
}

const a = await runProbe("probe A (sandbox=A, sibling=B)", {
  uid: UID_A,
  gid: GID_A,
  sandbox: A,
  sibling: B,
});
const b = await runProbe("probe B (sandbox=B, sibling=A) — fresh process, different UID", {
  uid: UID_B,
  gid: GID_B,
  sandbox: B,
  sibling: A,
});

const pass = a && b;
console.log(`\n=== setpriv isolation gate (${FILESYSTEM}): ${pass ? "PASS" : "FAIL"} ===`);
if (!pass) process.exit(1);

// ---- Sequential state-bleed regression (run A -> run B -> resume A) ----
console.log("\n=== HOR-381 sequential state-bleed regression ===");
const runState = (label, sandbox, uid, gid, mode) => {
  const child = launchChild({
    script: "/app/probe-state.mjs",
    uid,
    gid,
    sandboxRoot: sandbox,
    workingDir: "home",
    stdio: ["ignore", "pipe", "pipe"],
    env: { SANDBOX_ROOT: sandbox, MODE: mode, HOME: `${sandbox}/home` },
  });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (d) => (stdout += d));
  child.stderr.on("data", (d) => (stderr += d));
  return new Promise((resolve) => {
    child.on("close", (code) => {
      console.log(`\n--- ${label} (mode=${mode}, uid=${uid}) exit=${code} ---`);
      process.stdout.write(stdout);
      if (stderr) process.stderr.write(stderr);
      const state = {};
      for (const line of stdout.split("\n")) {
        const m = line.match(/^STATE (\w+)=(.*)$/);
        if (m) state[m[1]] = m[2];
      }
      resolve({ code, state });
    });
  });
};

// A fresh sandbox dir for the state probe (own session subdir).
sh(`mkdir -p ${A}/session ${B}/session`);
sh(`chown -R ${UID_A}:${GID_A} ${A} && chmod 0700 ${A}`);
sh(`chown -R ${UID_B}:${GID_B} ${B} && chmod 0700 ${B}`);

const aRun = await runState("runA", A, UID_A, GID_A, "runA");
const bRun = await runState("runB", B, UID_B, GID_B, "runB");
const aResume = await runState("resumeA", A, UID_A, GID_A, "resumeA");

const bleedPass =
  aRun.code === 0 &&
  bRun.code === 0 &&
  aResume.code === 0 &&
  aRun.state.mark === "A-mutated" && // A mutated in-memory state
  bRun.state.bleed === "no" && // B (fresh process) saw zero bleed
  aResume.state.pvcRestored === "yes" && // PVC marker survived
  aResume.state.memory === "initial" && // in-memory NOT restored
  aResume.state.memoryRestored === "no"; // only PVC state restores

console.log(`\n=== sequential state-bleed regression (${FILESYSTEM}): ${bleedPass ? "PASS" : "FAIL"} ===`);
process.exit(bleedPass ? 0 : 1);
