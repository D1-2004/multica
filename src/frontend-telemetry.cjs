'use strict';
const v8 = require('node:v8');
const fs = require('node:fs');
// Do not collect request bodies, cookies or environment values in diagnostics.
setInterval(() => {
  const { heapUsed, rss } = process.memoryUsage();
  const limit = v8.getHeapStatistics().heap_size_limit;
  const high = heapUsed / limit >= 0.85;
  const line = JSON.stringify({ time: new Date().toISOString(), level: high ? 'WARN' : 'INFO',
    msg: high ? 'frontend_heap_pressure' : 'frontend_memory',
    event: high ? 'frontend_heap_pressure' : 'frontend_memory', component: 'frontend',
    pid: process.pid, heap_used_bytes: heapUsed, heap_limit_bytes: limit, rss_bytes: rss }) + '\n';
  process.stdout.write(line);
  if (high && process.env.MULTICA_FRONTEND_ALERT_LOG) {
    try { fs.appendFileSync(process.env.MULTICA_FRONTEND_ALERT_LOG, line); }
    catch (error) { process.stderr.write(`frontend_alert_write_failed: ${error.code}\n`); }
  }
}, 30000).unref();
