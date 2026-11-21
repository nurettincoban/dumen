// Live sessions: real headless Claude Code with the plugin, several at once, on real git
// repositories. They need the claude CLI and an account, so they run by hand, not in CI:
//
//   node test/live.mjs [scenario...]        (DUMEN_LIVE_MODEL picks the model)
//
// Each scenario writes its transcripts to docs/transcripts/, in the shape Claude Code shows
// them, and exits non-zero if the agents did not do what the scenario expects.
//
// Who answers the prompt: these sessions are headless, so no person is there to see Claude
// Code's permission prompt. A PermissionRequest hook in each repository's settings answers
// Yes in the person's place, and logs the command it was asked about. Everything before that
// answer (dumen's guard, the words shown, the ticket) is the real path; the transcripts mark
// the answer as "The person answers Yes".
import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PLUGIN = path.join(HERE, '..', 'plugin');
const OUT = path.join(HERE, '..', 'docs', 'transcripts');
const MODEL = process.env.DUMEN_LIVE_MODEL || 'sonnet';
const DUMEN = path.join(PLUGIN, 'bin', 'dumen.mjs');

function sh(cwd, cmd, args) { return execFileSync(cmd, args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim(); }

function world() {
  const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'dumen-live-')));
  const store = path.join(tmp, 'git', 'shop-dumen.git');
  fs.mkdirSync(path.dirname(store), { recursive: true });
  sh(tmp, 'git', ['init', '-q', '--bare', store]);
  return { tmp, store };
}

function repo(w, name, email, files) {
  const origin = path.join(w.tmp, 'git', `${name}.git`);
  fs.mkdirSync(path.dirname(origin), { recursive: true });
  sh(w.tmp, 'git', ['init', '-q', '--bare', origin]);
  const r = path.join(w.tmp, email.split('@')[0], name);
  fs.mkdirSync(path.dirname(r), { recursive: true });
  sh(w.tmp, 'git', ['clone', '-q', origin, r]);
  sh(r, 'git', ['config', 'user.email', email]);
  sh(r, 'git', ['config', 'user.name', email.split('@')[0]]);
  for (const [f, text] of Object.entries(files)) { fs.mkdirSync(path.dirname(path.join(r, f)), { recursive: true }); fs.writeFileSync(path.join(r, f), text); }
  sh(r, 'git', ['add', '-A']);
  sh(r, 'git', ['commit', '-q', '-m', 'init']);
  sh(r, 'git', ['push', '-q', 'origin', 'HEAD']);
  // The person's "Yes" when Claude Code asks: headless, a PermissionRequest hook answers.
  fs.mkdirSync(path.join(r, '.claude'), { recursive: true });
  fs.writeFileSync(path.join(r, '.claude', 'settings.local.json'), JSON.stringify({
    hooks: { PermissionRequest: [{ matcher: 'Bash', hooks: [{ type: 'command', command: "node -e \"let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{const i=JSON.parse(s);require('fs').appendFileSync(process.env.HOME+'/.dumen-live-prompts','PROMPT '+JSON.stringify(i.tool_input.command)+'\\n');process.stdout.write(JSON.stringify({hookSpecificOutput:{hookEventName:'PermissionRequest',decision:{behavior:'allow'}}}))})\"" }] }] },
  }));
  return r;
}

function claude(cwd, prompt, opts = {}) {
  const args = ['-p', prompt, '--model', MODEL, '--plugin-dir', PLUGIN, '--output-format', 'stream-json', '--verbose',
    '--settings', path.join(cwd, '.claude', 'settings.local.json'), '--permission-prompts', 'none', ...(opts.args || [])];
  return new Promise((resolve, reject) => {
    const p = spawn('claude', args, { cwd, env: { ...process.env }, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    p.stdout.on('data', (d) => (out += d));
    p.stderr.on('data', (d) => (err += d));
    const timer = setTimeout(() => p.kill(), 8 * 60 * 1000);
    p.on('close', (code) => {
      clearTimeout(timer);
      if (code) return reject(new Error(`claude exited ${code}: ${err}`));
      resolve(transcript(out, opts.hide || []));
    });
  });
}

// transcript turns stream-json into what Claude Code shows: the prompt, tool calls, results,
// what hooks added, and the agent's words.
function transcript(raw, hide) {
  let final = '';
  let skill = false;
  const lines = [];
  const scrub = (s) => hide.concat([[path.resolve(PLUGIN), '~/.claude/plugins/dumen']]).reduce((t, [a, b]) => t.split(a).join(b), String(s)).replace(/\/tmp\/claude-[^\s"']*\/scratchpad\//g, '/tmp/');
  for (const l of raw.split('\n')) {
    let ev;
    try { ev = JSON.parse(l); } catch { continue; }
    if (ev.type === 'result') final = ev.result;
    if (ev.type === 'system' && ev.subtype === 'hook_response' && ev.output) {
      const h = hookText(ev.hook_event || ev.hook_name || '', ev.output);
      if (h && (!h.startsWith('✔') || /asks the person/.test(lines[lines.length - 1] || ''))) lines.push(`  ⎿ ${indent(scrub(h))}`);
    }
    for (const c of ev.message?.content || []) {
      if (c.type === 'tool_use') {
        const shown = c.name === 'Bash' ? c.input.command.replace(/ --(ticket|session) \S+/g, '') : c.name === 'Skill' ? c.input.skill : JSON.stringify(c.input);
        lines.push(`⏺ ${c.name}(${scrub(shown)})`);
        skill = c.name === 'Skill';
      } else if (c.type === 'tool_result') { lines.push(`  ⎿ ${skill ? '(the dumen skill is loaded)' : indent(scrub(text(c.content)))}`); skill = false; }
      else if (c.type === 'text' && c.text.trim() && !c.text.startsWith('Base directory for this skill') && !c.text.startsWith('Stop hook feedback')) lines.push(`⏺ ${indent(scrub(c.text))}`);
    }
  }
  return { final: scrub(final), text: lines.join('\n') };
}
// hookText shows what a hook added the way Claude Code shows it to the person.
function hookText(event, out) {
  let o = null;
  try { o = JSON.parse(out); } catch {}
  if (/PermissionRequest/.test(event)) return '✔ The person answers Yes.';
  if (!o) return out.trim() ? `[${event.replace(/:.*/, '')}] ${out.trim()}` : '';
  const h = o.hookSpecificOutput || {};
  if (h.permissionDecision === 'ask') return `Claude Code asks the person: ${h.permissionDecisionReason}`;
  if (h.permissionDecision === 'deny') return `Blocked: ${h.permissionDecisionReason}`;
  if (h.additionalContext) return `[dumen] ${h.additionalContext}`;
  if (o.decision === 'block') return `[dumen, before stopping] ${o.reason}`;
  return '';
}
function text(c) { return typeof c === 'string' ? c : (c || []).map((x) => x.text || '').join('\n'); }
function indent(s) { return String(s).trim().replace(/\n/g, '\n    '); }

function save(name, prompt, t) {
  fs.mkdirSync(OUT, { recursive: true });
  fs.writeFileSync(path.join(OUT, `${name}.txt`), `> ${prompt}\n\n${t.text}\n`);
  console.log(`\n===== ${name} =====\n> ${prompt}\n\n${t.text}\n`);
}

function check(ok, what) { if (!ok) { console.error(`FAILED: ${what}`); process.exitCode = 1; } else console.log(`ok: ${what}`); }

const hideTmp = (w) => [[w.tmp, '/home'], [os.homedir(), '~']];

const scenarios = {
  // One repository, nothing configured: the person says one sentence.
  async setup() {
    const w = world();
    const r = repo(w, 'shop', 'maya@example.com', { 'src/booking.ts': 'export function book() {}\n' });
    const prompt = 'Set up dumen for this project.';
    const t = await claude(r, prompt, { hide: hideTmp(w) });
    save('setup', prompt, t);
    check(sh(r, 'git', ['ls-remote', '--heads', 'origin', 'dumen']).includes('refs/heads/dumen'), 'the store branch exists on origin');
  },

  // The frontend makes a UI decision that needs work from the backend. The backend agent,
  // in the middle of its own work, hears it in the same session, with the ask.
  async frontend() {
    const w = world();
    const web = repo(w, 'web-app', 'sofia@example.com', { 'src/TimePicker.tsx': 'export const TimePicker = () => null;\n' });
    const booking = repo(w, 'booking-svc', 'ayse@example.com', {
      'src/slots.go': 'package slots\n\n// List returns the open slots of a day.\nfunc List() []string { return nil }\n',
      'check.sh': 'sleep 40\necho "go vet: ok"\n',
    });
    for (const [r, svc] of [[web, 'web-app'], [booking, 'booking-svc']]) sh(r, 'node', [DUMEN, 'init', '--store', w.store, '--service', svc]);
    const backendPrompt = 'Run `sh check.sh` once and wait for it (about 40 seconds). Then read src/slots.go. Then tell me in a few lines whether anything from the team changed while you worked and what it means for this service.';
    const frontendPrompt = 'You are working on the time picker. You asked me "How should the time picker show availability?" and I answered: slot buttons that show how many tables are left. ' +
      'The backend does not return that yet; booking-svc would have to return remaining tables per slot in GET /slots. Save my answer for the team and put it in force, I agree.';
    const back = claude(booking, backendPrompt, { hide: hideTmp(w) });
    await new Promise((r) => setTimeout(r, 12000));
    const front = await claude(web, frontendPrompt, { hide: hideTmp(w) });
    const b = await back;
    save('frontend-web-app', frontendPrompt, front);
    save('frontend-booking-svc', backendPrompt, b);
    check(/D-[A-Z0-9]{4}/.test(front.final), 'the web-app agent reports the decision id');
    check(/DECISION NEWS/.test(b.text) && /NEEDS FROM booking-svc/.test(b.text), 'the running booking-svc session got the news with the ask');
    check(/remaining|tables left|tables are left/i.test(b.final), 'the booking-svc agent tells its human what is needed');
  },

  // Two backend developers on the same product. Billing changes the refund rule while
  // booking is building the cancel endpoint; booking first finds the old rule without
  // asking, then hears the change mid-session.
  async backends() {
    const w = world();
    const billing = repo(w, 'billing-svc', 'deniz@example.com', { 'src/refund.go': 'package refund\n' });
    const booking = repo(w, 'booking-svc', 'ayse@example.com', { 'src/cancel.go': 'package booking\n\n// Cancel cancels a booking.\nfunc Cancel(id string) error { return nil }\n', 'test.sh': 'sleep 40\necho "PASS"\n' });
    for (const [r, svc] of [[billing, 'billing-svc'], [booking, 'booking-svc']]) sh(r, 'node', [DUMEN, 'init', '--store', w.store, '--service', svc]);
    const seed = claude(billing, 'Record this team decision and put it in force, I agree: cancelled bookings are refunded as store credit (kind product, topics payments). Why: it keeps the money in the product. Rejected: refund to the card.', { hide: hideTmp(w) });
    const s = await seed;
    save('backends-seed', 'Record this team decision and put it in force, I agree: cancelled bookings are refunded as store credit ...', s);
    const bookingPrompt = 'I am building the cancel endpoint in src/cancel.go. Before you ask me whether a cancelled booking is refunded to the card or as store credit, check whether the team already decided it and tell me which decision you follow. Then run `sh test.sh` once (about 40 seconds) and, when it is done, tell me in a few lines whether anything changed for the cancel endpoint.';
    const changePrompt = 'Accounting says refunds must go back to the card, not store credit. Change the team decision on refunds for cancelled bookings, I agree.';
    const b = claude(booking, bookingPrompt, { hide: hideTmp(w) });
    await new Promise((r) => setTimeout(r, 25000));
    const ch = await claude(billing, changePrompt, { hide: hideTmp(w) });
    const bk = await b;
    save('backends-billing', changePrompt, ch);
    save('backends-booking', bookingPrompt, bk);
    check(/store credit/i.test(bk.text), 'booking found the decision before asking');
    check(/DECISION NEWS[\s\S]*replaces/i.test(bk.text), 'booking heard the replacement mid-session');
    check(/card/i.test(bk.final), 'booking tells its human the refund now goes to the card');
  },

  // A design becomes a source of truth in git, with its summary; its decisions follow.
  async design() {
    const w = world();
    const web = repo(w, 'web-app', 'sofia@example.com', { 'src/Checkout.tsx': 'export const Checkout = () => null;\n' });
    sh(web, 'node', [DUMEN, 'init', '--store', w.store, '--service', 'web-app']);
    const prompt = 'This Claude Design file is the source of truth for the checkout screens: https://claude.ai/design/p/9f3c-checkout (I cannot connect it here, so here is what it says: ' +
      'the checkout has three steps, Time, Details, Pay; the Time step groups slots into Lunch, Afternoon and Dinner; each slot is a button with its time; the Pay step shows a 10 minute hold timer). ' +
      'Keep it for the team as a source, and propose the decisions it implies.';
    const t = await claude(web, prompt, { hide: hideTmp(w) });
    save('design-source', prompt, t);
    check(fs.existsSync(path.join(web, '.git', 'dumen', 'store', 'sources')), 'the design is kept as a source');
    check(/propose/.test(t.text), 'the agent proposed decisions from it');
  },

  // A person who allowed every dumen command (Bash(dumen:*), even Bash(dumen approve:*)) in
  // their settings, and nobody to answer a prompt: the approval must not land. dumen's hook asks,
  // and an ask is not skipped by an allow rule. If a Claude Code release ever changes that, this
  // scenario fails.
  async allowrule() {
    const w = world();
    const web = repo(w, 'web-app', 'sofia@example.com', { 'src/booking.ts': 'export function book() {}\n' });
    sh(web, 'node', [DUMEN, 'init', '--store', w.store, '--service', 'web-app']);
    const id = sh(web, 'node', [DUMEN, 'propose', '--kind', 'technical', '--rule', 'Every booking request carries an idempotency key']).match(/D-[A-Z0-9]+/)[0];
    // No PermissionRequest hook here: these are the person's own settings, with no one at the keyboard.
    fs.writeFileSync(path.join(web, '.claude', 'settings.local.json'), JSON.stringify({ permissions: { allow: ['Bash(dumen:*)', 'Bash(dumen approve:*)'] } }));
    const prompt = `I agree with ${id}. Put it in force for the team now.`;
    const t = await claude(web, prompt, { hide: hideTmp(w) });
    save('allow-rule-headless', prompt, t);
    check(/proposed/.test(sh(web, 'node', [DUMEN, 'show', id])), `${id} is still proposed: an allow rule did not skip the person's yes`);
  },
};

const pick = process.argv.slice(2);
for (const name of pick.length ? pick : Object.keys(scenarios)) {
  console.log(`\n### ${name}`);
  try { await scenarios[name](); } catch (e) { console.error(e); process.exitCode = 1; }
}
