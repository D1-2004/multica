'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { once } = require('node:events');
const { startSupervisor, probe } = require('./frontend-supervisor.cjs');

async function until(fn, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await fn()) return;
    await new Promise(r => setTimeout(r, 20));
  }
  throw new Error('condition timed out');
}
async function freePort() {
  const s = http.createServer(); s.listen(0, '127.0.0.1'); await once(s, 'listening');
  const port = s.address().port; await new Promise(r => s.close(r)); return port;
}

test('frontend recovers from repeated exits and hangs; health fails and planned stop never respawns', async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'frontend-supervisor-test-'));
  fs.mkdirSync(path.join(dir, 'web'));
  const fixture = path.join(dir, 'web', 'fixture.cjs');
  fs.writeFileSync(fixture, `const http=require('node:http');let hung=false;
process.on('SIGUSR2',()=>{hung=true});
http.createServer((req,res)=>{if(!hung){res.end('ok')}}).listen(Number(process.env.PORT),'127.0.0.1');`);
  const backend = http.createServer((req,res) => res.end('ok'));
  backend.listen(0, '127.0.0.1'); await once(backend, 'listening');
  const frontendPort = await freePort();
  const supervisor = startSupervisor({ appRoot: dir, logDir: dir, runDir: dir,
    frontendPort, backendPort: backend.address().port, healthPort: 0,
    args: [fixture], intervalMs: 40, startupGraceMs: 500, probeTimeoutMs: 80,
    stopGraceMs: 80, restartBaseMs: 100, restartMaxMs: 300 });
  t.after(async () => {
    supervisor.stop();
    await until(() => !supervisor.getChild());
    await new Promise(r => backend.close(r));
    fs.rmSync(dir, { recursive: true, force: true });
  });
  await once(supervisor.server, 'listening');
  const healthPort = supervisor.server.address().port;
  await until(() => probe(healthPort, '/check.node'));
  for (let i=0;i<2;i++) {
    const old = supervisor.getChild(); old.kill('SIGKILL');
    await until(() => supervisor.getChild() !== old);
    assert.equal(await probe(healthPort, '/check.node'), false);
    await until(() => probe(healthPort, '/check.node'));
    assert.notEqual(supervisor.getChild().pid, old.pid);
  }
  const hung = supervisor.getChild(); hung.kill('SIGUSR2');
  await until(() => supervisor.getChild() !== hung);
  await until(() => probe(healthPort, '/check.node'));
  await until(() => fs.readFileSync(path.join(dir,'backend.log'),'utf8').includes('frontend_recovered'));
  await supervisor.check();
  const events = fs.readFileSync(path.join(dir,'backend.log'),'utf8').trim().split('\n').map(JSON.parse);
  assert.equal(events.filter(x=>x.event==='frontend_exited').length,3);
  assert.ok(events.some(x=>x.event==='frontend_unhealthy'));
  assert.ok(events.some(x=>x.event==='frontend_recovered' && x.restart_count>=2));
  supervisor.stop(); await until(() => !supervisor.getChild());
  await new Promise(r=>setTimeout(r,400));
  assert.equal(supervisor.getChild(),null);
});

test('health rejects 404 and times out a response that never ends', async () => {
  for (const handler of [(req,res)=>{res.writeHead(404);res.end()}, (req,res)=>{res.writeHead(200);res.write('partial')}]) {
    const s=http.createServer(handler);s.listen(0,'127.0.0.1');await once(s,'listening');
    assert.equal(await probe(s.address().port,'/',40),false);
    s.closeAllConnections();await new Promise(r=>s.close(r));
  }
});
