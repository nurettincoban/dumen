const { spawn } = require('node:child_process');
const { createHash, randomUUID } = require('node:crypto');
const { constants } = require('node:fs');
const { access, mkdir, rename, rm, writeFile } = require('node:fs/promises');
const { homedir } = require('node:os');
const { delimiter, isAbsolute, join } = require('node:path');

const platforms = { darwin: 'darwin', linux: 'linux', win32: 'windows' };
const architectures = { x64: 'amd64', arm64: 'arm64' };
const binaryName = process.platform === 'win32' ? 'dumen.exe' : 'dumen';
const release = process.env.DUMEN_RELEASE_URL ||
  'https://github.com/nurettincoban/dumen/releases/latest/download';
const dataDir = process.env.CLAUDE_PLUGIN_DATA || join(
  process.env.XDG_DATA_HOME || join(homedir(), '.local', 'share'), 'dumen-plugin',
);

async function isExecutable(path) {
  try {
    await access(path, constants.X_OK);
    return true;
  } catch (error) {
    if (['ENOENT', 'EACCES', 'ENOTDIR'].includes(error.code)) return false;
    throw error;
  }
}

async function download(name) {
  const response = await fetch(`${release.replace(/\/$/, '')}/${name}`, {
    signal: AbortSignal.timeout(60_000),
  });
  if (!response.ok) throw new Error(`could not download ${name}: HTTP ${response.status}`);
  return Buffer.from(await response.arrayBuffer());
}

async function install(binary) {
  const os = platforms[process.platform];
  const arch = architectures[process.arch];
  if (!os || !arch) throw new Error(`no release binary for ${process.platform}/${process.arch}`);

  const extension = process.platform === 'win32' ? '.exe' : '';
  const name = `dumen_${os}_${arch}${extension}`;
  const [contents, checksums] = await Promise.all([download(name), download('checksums.txt')]);
  const expected = checksums.toString().split(/\r?\n/).map(line => line.trim().split(/\s+/))
    .find(([, file]) => file === name)?.[0];
  const actual = createHash('sha256').update(contents).digest('hex');
  if (expected !== actual) throw new Error(`checksum mismatch for ${name}`);

  await mkdir(dataDir, { recursive: true });
  const temporary = join(dataDir, `.download-${randomUUID()}`);
  try {
    await writeFile(temporary, contents, { flag: 'wx', mode: 0o755 });
    try {
      await rename(temporary, binary);
    } catch (error) {
      // Another session may already be running the verified binary, especially on Windows.
      if (!(await isExecutable(binary))) throw error;
    }
  } finally {
    await rm(temporary, { force: true });
  }
}

async function run() {
  const args = process.argv.slice(2);
  const paths = (process.env.PATH || '').split(delimiter);
  let binary = join(dataDir, binaryName);
  for (const dir of paths.filter(isAbsolute)) {
    const candidate = join(dir, binaryName);
    if (await isExecutable(candidate)) {
      binary = candidate;
      break;
    }
  }
  if (!(await isExecutable(binary))) {
    if (args[0] !== 'mcp') return; // Hooks let the MCP process handle the first install.
    await install(binary);
  }
  const child = spawn(binary, args, { stdio: 'inherit', windowsHide: true });
  child.on('error', report);
  child.on('exit', code => { process.exitCode = code ?? 1; });
}

function report(error) {
  process.stderr.write(`dumen: ${error.message}\n`);
  process.exitCode = 1;
}

run().catch(report);
