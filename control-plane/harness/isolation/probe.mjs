// Isolation probe child (HOR-381). Launched via the REAL setpriv launcher
// (harness/src/launcher.ts -> dist/launcher.js) under a per-session UID/GID.
// Asserts the kernel-enforced isolation the warm-worker design depends on:
//   - dropped privileges (no caps, no_new_privs, no supplementary groups)
//   - own-sandbox read/write
//   - sibling-session EACCES via Node fs, shell, symlink, and a subprocess
//   - the shared mount root is traversable but not listable (0711)
//
// Config arrives via env (the launcher's spawn({env}) — the supervisor's env is
// not inherited). Prints `PASS/FAIL <assertion>` lines and exits 0 only if all
// pass. This is the HOR-381 gate (bullets 1-5); bullets 6-7 (RWO local-path cross-pod
// ownership + Pod Security admission) are HOR-245's gate.

import fs from "node:fs";
import { execSync, spawnSync } from "node:child_process";

const SANDBOX = process.env.SANDBOX_ROOT;
const SIBLING = process.env.SIBLING_ROOT;
const MOUNT = process.env.MOUNT_ROOT;
const TLS_KEY = process.env.TLS_KEY;

if (!SANDBOX || !SIBLING || !MOUNT || !TLS_KEY) {
  console.error("probe: SANDBOX_ROOT, SIBLING_ROOT, MOUNT_ROOT, TLS_KEY env vars are required");
  process.exit(2);
}

const lines = [];
const ok = (label, cond) => lines.push(`${cond ? "PASS" : "FAIL"}  ${label}`);

// --- privilege state (parse /proc/self/status) ---
const status = fs.readFileSync("/proc/self/status", "utf8");
// [ \t]+ (not \s+) so an empty field like `Groups:\t ` doesn't match across the
// newline into the next field's name.
const field = (name) => status.match(new RegExp(`^${name}:[ \t]+(\\S+)`, "m"))?.[1];

ok("NoNewPrivs=1", field("NoNewPrivs") === "1");
ok("CapEff=0 (no effective capabilities)", field("CapEff") === "0000000000000000");
ok("CapPrm=0 (no permitted capabilities)", field("CapPrm") === "0000000000000000");
ok("CapBnd=0 (bounding set cleared)", field("CapBnd") === "0000000000000000");
ok("CapAmb=0 (no ambient capabilities)", field("CapAmb") === "0000000000000000");
ok(`uid=${process.getuid()} gid=${process.getgid()} (stable UID=GID, non-root)`, process.getuid() > 0 && process.getuid() === process.getgid());
// /proc/self/status `Groups:` lists supplementary groups only (not the primary
// GID). --clear-groups empties it; node's process.getgroups() also reports the
// effective GID, so we read /proc rather than getgroups().
ok("no supplementary groups (--clear-groups)", (field("Groups") ?? "").trim() === "");
ok("umask=0077", process.umask() === 0o077);

// --- exact pool/session ownership and modes ---
const mountStat = fs.lstatSync(MOUNT);
ok("pool PVC root is root-owned 0711", mountStat.uid === 0 && !mountStat.isSymbolicLink() && mountStat.isDirectory() && (mountStat.mode & 0o777) === 0o711);
for (const name of ["root", "home", "tmp", "session", "workspace"]) {
  const path = name === "root" ? SANDBOX : `${SANDBOX}/${name}`;
  const stat = fs.lstatSync(path);
  ok(`${name} is session-owned 0700`, !stat.isSymbolicLink() && stat.isDirectory() && stat.uid === process.getuid() && stat.gid === process.getgid() && (stat.mode & 0o777) === 0o700);
}

// --- own-sandbox read/write ---
let w = true;
try {
  fs.writeFileSync(`${SANDBOX}/session/probe.txt`, "hello");
} catch {
  w = false;
}
let r = true;
try {
  r = fs.readFileSync(`${SANDBOX}/session/probe.txt`, "utf8") === "hello";
} catch {
  r = false;
}
ok("own-sandbox write+read", w && r);

// --- mount root is traversable but NOT listable (0711) ---
let listErr = false;
try {
  fs.readdirSync(MOUNT);
} catch (e) {
  listErr = e.code === "EACCES";
}
ok("cannot readdir mount root (0711 — traverse only)", listErr);

// --- sibling-session EACCES via Node fs ---
let statSib = false;
try {
  fs.statSync(`${SIBLING}/session/secret.txt`);
} catch (e) {
  statSib = e.code === "EACCES";
}
ok("cannot stat sibling session file (EACCES)", statSib);
let readSib = false;
try {
  fs.readFileSync(`${SIBLING}/session/secret.txt`, "utf8");
} catch (e) {
  readSib = e.code === "EACCES";
}
ok("cannot read sibling session file (EACCES)", readSib);

// The cert-manager CSI key is mounted in the supervisor container namespace,
// but exact root:0440 permissions must make the real child open fail EACCES.
let keyDenied = false;
try {
  fs.openSync(TLS_KEY, "r");
} catch (e) {
  keyDenied = e.code === "EACCES";
}
ok("cannot open supervisor tls.key (EACCES)", keyDenied);

// --- sibling-session EACCES via shell ---
let shellLs = true;
try {
  execSync(`ls ${SIBLING}`, { stdio: "ignore" });
} catch {
  shellLs = false;
}
ok("shell `ls <sibling>` fails", shellLs === false);
let shellCat = true;
try {
  execSync(`cat ${SIBLING}/session/secret.txt`, { stdio: "ignore" });
} catch {
  shellCat = false;
}
ok("shell `cat <sibling>/secret` fails", shellCat === false);

// --- sibling-session EACCES via symlink (kernel follows, checks target perms) ---
let sym = false;
try {
  fs.symlinkSync(`${SIBLING}/session/secret.txt`, `${SANDBOX}/session/lnk`);
  fs.readFileSync(`${SANDBOX}/session/lnk`, "utf8");
} catch (e) {
  sym = e.code === "EACCES";
}
ok("symlink to sibling fails (EACCES)", sym);

// --- sibling-session EACCES via a spawned subprocess ---
const sub = spawnSync(process.execPath, ["-e", `require("node:fs").readFileSync("${SIBLING}/session/secret.txt")`], {
  stdio: "pipe",
});
ok("subprocess read of sibling fails (non-zero exit)", sub.status !== 0);

console.log(lines.join("\n"));
process.exit(lines.every((l) => l.startsWith("PASS")) ? 0 : 1);
