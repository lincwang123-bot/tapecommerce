// SPDX-License-Identifier: GPL-3.0-only
import { createServer } from 'node:http';
import { loadQuoteConfig } from './quote-config.mjs';
import { createQuoteHandler } from './quote-server.mjs';

try {
  const config=await loadQuoteConfig();
  if(!config.enabled)throw Object.assign(new Error(),{code:'quote_service_disabled'});
  const server=createServer({maxHeaderSize:8192},createQuoteHandler(config));
  server.headersTimeout=5000;server.requestTimeout=20000;server.keepAliveTimeout=1000;server.maxRequestsPerSocket=1;
  server.on('clientError',(_error,socket)=>socket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n'));
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(config.port,config.host,resolve);});
  console.log(JSON.stringify({service:'tapecommerce-private-quotes',listening:true,host:config.host,port:config.port}));
  let closing=false;
  const close=()=>{if(closing)return;closing=true;server.close(()=>{clearTimeout(timer);process.exitCode=0;});const timer=setTimeout(()=>server.closeAllConnections(),35000);timer.unref();};
  process.once('SIGTERM',close);process.once('SIGINT',close);
} catch(error) {
  // Never log an upstream error, a secret-file path or signer material.
  console.error(JSON.stringify({error:typeof error?.code==='string'&&error.code.startsWith('quote_')?error.code:'quote_startup_failed'}));process.exitCode=1;
}
