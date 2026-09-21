'use strict';

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

function probe(port, pathname, timeout = 1500) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve(ok);
    };
    const req = http.get({ hostname: '127.0.0.1', port, path: pathname }, (res) => {
      res.resume();
      res.on('end', () => finish(res.statusCode >= 200 && res.statusCode < 400));
      res.on('error', () => finish(false));
    });
    const timer = setTimeout(() => { finish(false); req.destroy(); }, timeout);
    req.on('error', () => finish(false));
  });
}

function startSupervisor(options) {
  const {
    appRoot, logDir, runDir, frontendPort = 3000, backendPort = 8080,
    healthPort = 6001, intervalMs = 5000, startupGraceMs = 30000,
    stopGraceMs = 5000, probeTimeoutMs = 1500, restartBaseMs = 1000, restartMaxMs = 60000,
    command = process.execPath,
    args = ['--require', path.join(__dirname, 'frontend-telemetry.cjs'), 'apps/web/server.js'],
  } = options;
  let child, stopping = false, failures = 0, restartCount = 0, startedAt = 0;
  let restartTimer, killTimer, checking = false, frontendReady = false, backendReady = false;
  let healthySince = 0, failureStreak = 0;
  const pidFile = path.join(runDir, 'frontend.pid');
  const stateFile = path.join(runDir, 'frontend-status.json');
  const alertFile = path.join(logDir, 'backend.log');
  function event(level, name, fields = {}) {
    const line = JSON.stringify({ time: new Date().toISOString(), level, msg: name,
      event: name, component: 'frontend_supervisor', ...fields }) + '\n';
    process.stdout.write(line);
    // Use the existing backend SLS collection and retain events across restarts.
    try { fs.appendFileSync(alertFile, line); } catch (error) {
      process.stderr.write(`frontend_alert_write_failed: ${error.code}\n`);
    }
  }
  function state() {
    const value = { frontendReady, backendReady, restartCount, pid: child?.pid || null,
      stopping, checkedAt: new Date().toISOString() };
    fs.writeFileSync(stateFile + '.tmp', JSON.stringify(value));
    fs.renameSync(stateFile + '.tmp', stateFile);
  }
  function launch() {
    if (stopping) return;
    const logfile = path.join(logDir, 'frontend.log');
    // Bound retained logs and keep the previous failure instead of truncating it.
    if (fs.existsSync(logfile) && fs.statSync(logfile).size > 10 * 1024 * 1024) {
      fs.renameSync(logfile, logfile + '.previous');
    }
    const fd = fs.openSync(logfile, 'a');
    child = spawn(command, args, { cwd: path.join(appRoot, 'web'),
      env: { ...process.env, PORT: String(frontendPort), HOSTNAME: '0.0.0.0',
        MULTICA_FRONTEND_ALERT_LOG: alertFile }, stdio: ['ignore', fd, fd] });
    fs.closeSync(fd);
    startedAt = Date.now(); failures = 0; frontendReady = false;
    if (child.pid) fs.writeFileSync(pidFile, String(child.pid));
    event('INFO', 'frontend_started', { pid: child.pid || null, restart_count: restartCount });
    let handled = false;
    const exited = (code, signal, error) => {
      if (handled) return;
      handled = true;
      clearTimeout(killTimer); killTimer = null;
      child = null; frontendReady = false; healthySince = 0;
      fs.rmSync(pidFile, { force: true });
      state();
      if (stopping) { server.close(); return; }
      failureStreak++;
      const delay = Math.min(restartMaxMs, restartBaseMs * 2 ** Math.min(failureStreak - 1, 10));
      event('ERROR', 'frontend_exited', { exit_code: code, signal, error,
        restart_count: restartCount, retry_after_ms: delay });
      restartTimer = setTimeout(() => { restartCount++; launch(); }, delay);
    };
    child.once('exit', (code, signal) => exited(code, signal));
    child.once('error', (err) => exited(null, null, err.code));
  }
  function terminateChild(reason) {
    if (!child || killTimer) return;
    if (!stopping) event('ERROR', 'frontend_unhealthy', { reason, pid: child.pid });
    const target = child;
    target.kill('SIGTERM');
    killTimer = setTimeout(() => { target.kill('SIGKILL'); killTimer = null; }, stopGraceMs);
  }
  async function check() {
    if (checking || stopping) return;
    checking = true;
    try {
      const observedChild = child;
      [backendReady, frontendReady] = await Promise.all([
        probe(backendPort, '/healthz', probeTimeoutMs), child ? probe(frontendPort, '/login', probeTimeoutMs) : false,
      ]);
      if (child !== observedChild || stopping) frontendReady = false;
      if (frontendReady) {
        failures = 0;
        if (!healthySince) {
          healthySince = Date.now();
          event('INFO', 'frontend_recovered', { pid: child?.pid, restart_count: restartCount });
        }
        if (Date.now() - healthySince > 60000) failureStreak = 0;
      } else {
        healthySince = 0;
        if (child && Date.now() - startedAt >= startupGraceMs && ++failures >= 3) {
          terminateChild('http_probe_failed');
        }
      }
      state();
    } finally { checking = false; }
  }
  const server = http.createServer(async (req, res) => {
    if (!['/check.node', '/healthz'].includes(req.url?.split('?')[0])) {
      res.writeHead(404); res.end('not found'); return;
    }
    // Probe each health request: never return stale success after a crash.
    const [backend, frontend] = await Promise.all([
      probe(backendPort, '/healthz', probeTimeoutMs), child && !stopping ? probe(frontendPort, '/login', probeTimeoutMs) : false,
    ]);
    res.writeHead(backend && frontend && !stopping ? 200 : 503, { 'content-type': 'text/plain' });
    res.end(backend && frontend && !stopping ? 'success' : `backend=${backend} frontend=${frontend}`);
  });
  const interval = setInterval(check, intervalMs);
  function stop() {
    if (stopping) return;
    stopping = true; clearInterval(interval); clearTimeout(restartTimer);
    event('INFO', 'frontend_supervisor_stopping');
    if (child) terminateChild('shutdown'); else server.close();
  }
  server.on('error', () => { stop(); process.exitCode = 1; });
  server.listen(healthPort, '0.0.0.0');
  launch();
  return { stop, server, check, getChild: () => child };
}

if (require.main === module) {
  const root = path.resolve(__dirname, '..');
  const appName = process.env.APP_NAME;
  if (!appName) throw new Error('APP_NAME is required');
  const supervisor = startSupervisor({ appRoot: root,
    logDir: `/home/admin/${appName}/logs`, runDir: `/home/admin/${appName}/run`,
    frontendPort: Number(process.env.FRONTEND_PORT || 3000),
    backendPort: Number(process.env.BACKEND_PORT || 8080),
    healthPort: Number(process.env.AONE_HEALTH_PORT || 6001) });
  process.on('SIGTERM', supervisor.stop);
  process.on('SIGINT', supervisor.stop);
}
module.exports = { startSupervisor, probe };
