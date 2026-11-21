// dumen-install downloads the dumen release binary for this system into the plugin's data
// directory, after checking it against the release's checksums.txt. Run it once, by hand:
//
//   node <plugin root>/bin/dumen-install.cjs [--dir <plugin data dir>] [--force]
//
// It is only an installer. The plugin's hooks and MCP server start the native binary at
// ${CLAUDE_PLUGIN_DATA}/dumen directly, so that dumen's parent process is Claude Code itself:
// dumen binds sessions and approval tickets to that parent.
'use strict';
const { createHash, randomUUID } = require('node:crypto');
const { chmod, mkdir, rename, rm, stat, writeFile } = require('node:fs/promises');
const { homedir } = require('node:os');
const { join, resolve } = require('node:path');

const platforms = { darwin: 'darwin', linux: 'linux', win32: 'windows' };
const architectures = { x64: 'amd64', arm64: 'arm64' };
const extension = process.platform === 'win32' ? '.exe' : '';
const release = (process.env.DUMEN_RELEASE_URL ||
  'https://github.com/nurettincoban/dumen/releases/latest/download').replace(/\/+$/, '');

function usage(message) {
  if (message) process.stderr.write(`dumen-install: ${message}\n`);
  process.stderr.write('usage: node dumen-install.cjs [--dir <plugin data dir>] [--force]\n');
  process.exit(2);
}

function parse(argv) {
  const options = { dir: '', force: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '--force') options.force = true;
    else if (arg === '--dir') {
      if (!argv[i + 1]) usage('--dir needs a path');
      options.dir = argv[++i];
    } else if (arg === '-h' || arg === '--help') usage();
    else usage(`unknown argument ${arg}`);
  }
  // The plugin's data directory, as Claude Code names it for dumen@dumen.
  options.dir = resolve(options.dir || process.env.CLAUDE_PLUGIN_DATA || join(
    process.env.CLAUDE_CONFIG_DIR || join(homedir(), '.claude'), 'plugins', 'data', 'dumen-dumen'));
  return options;
}

async function exists(path) {
  try {
    return (await stat(path)).isFile();
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return false;
    throw error;
  }
}

async function download(name) {
  const response = await fetch(`${release}/${name}`, { signal: AbortSignal.timeout(120_000) });
  if (!response.ok) throw new Error(`could not download ${name}: HTTP ${response.status}`);
  return Buffer.from(await response.arrayBuffer());
}

async function main() {
  const { dir, force } = parse(process.argv.slice(2));
  const os = platforms[process.platform];
  const arch = architectures[process.arch];
  if (!os || !arch) {
    throw new Error(`no release binary for ${process.platform}/${process.arch}; ` +
      'build it with: go install github.com/nurettincoban/dumen/cmd/dumen@latest');
  }
  const binary = join(dir, `dumen${extension}`);
  if (!force && await exists(binary)) {
    process.stdout.write(`dumen is already installed at ${binary} (use --force to replace it)\n`);
    return;
  }

  const name = `dumen_${os}_${arch}${extension}`;
  const [contents, checksums] = await Promise.all([download(name), download('checksums.txt')]);
  const expected = checksums.toString('utf8').split(/\r?\n/)
    .map(line => line.trim().split(/\s+/))
    .find(([, file]) => file === name || file === `*${name}`)?.[0]?.toLowerCase();
  const actual = createHash('sha256').update(contents).digest('hex');
  if (!expected) throw new Error(`checksums.txt has no entry for ${name}; not installed`);
  if (expected !== actual) throw new Error(`checksum mismatch for ${name}; not installed`);

  await mkdir(dir, { recursive: true });
  const temporary = join(dir, `.download-${randomUUID()}`);
  try {
    await writeFile(temporary, contents, { flag: 'wx', mode: 0o755 });
    await chmod(temporary, 0o755);
    try {
      await rename(temporary, binary);
    } catch (error) {
      if (['EPERM', 'EBUSY', 'EACCES'].includes(error.code)) {
        throw new Error(`cannot replace ${binary} (${error.code}); ` +
          'quit the Claude Code sessions that use dumen and run this again');
      }
      throw error;
    }
  } finally {
    await rm(temporary, { force: true });
  }
  process.stdout.write(`installed ${name} (sha256 ${actual}) at ${binary}\n` +
    'Restart Claude Code so the plugin\'s hooks and MCP server start it.\n');
}

main().catch(error => {
  process.stderr.write(`dumen-install: ${error.message}\n`);
  process.exitCode = 1;
});
