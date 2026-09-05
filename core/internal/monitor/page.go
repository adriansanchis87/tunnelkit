package monitor

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Tunnelkit</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, noarchive, nosnippet">
<style>
  :root{color-scheme:light dark;--bg:#f5f6f8;--fg:#1b1f24;--muted:#62707f;--card:#fff;
    --border:#e1e4e8;--up:#1a7f37;--up-bg:#dafbe1;--down:#cf222e;--down-bg:#ffebe9;
    --warn:#9a6700;--warn-bg:#fff8c5;--rx:#1a7f37;--tx:#0969da;--accent:#0969da;}
  @media (prefers-color-scheme:dark){:root{--bg:#0d1117;--fg:#e6edf3;--muted:#8b949e;
    --card:#161b22;--border:#30363d;--up:#3fb950;--up-bg:#12261b;--down:#f85149;
    --down-bg:#2b1414;--warn:#d29922;--warn-bg:#2b2412;--rx:#3fb950;--tx:#58a6ff;--accent:#58a6ff;}}
  *{box-sizing:border-box}body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
    background:var(--bg);color:var(--fg);padding:1.5rem}
  h1{font-size:1.25rem;margin:0 0 .25rem}h3{margin:.8rem 0 .3rem;font-size:.95rem}.sub{color:var(--muted);font-size:.85rem;margin-bottom:1rem}
  .summary{display:flex;gap:.75rem;flex-wrap:wrap;margin-bottom:1rem}
  .pill{padding:.35rem .75rem;border-radius:999px;font-size:.85rem;background:var(--card);border:1px solid var(--border)}
  .wrap{overflow-x:auto}table{width:100%;border-collapse:collapse;background:var(--card);
    border:1px solid var(--border);border-radius:8px;overflow:hidden}
  th,td{text-align:left;padding:.5rem .8rem;border-bottom:1px solid var(--border);font-size:.85rem;white-space:nowrap;vertical-align:middle}
  th{color:var(--muted);font-weight:600}tr:last-child td{border-bottom:none}
  .badge{display:inline-flex;align-items:center;gap:.4rem;padding:.15rem .6rem;border-radius:999px;font-size:.8rem;font-weight:600}
  .up{color:var(--up);background:var(--up-bg)}
  .dot{width:.5rem;height:.5rem;border-radius:50%;background:currentColor}
  .mono{font-variant-numeric:tabular-nums}.muted{color:var(--muted)}
  .hi{color:var(--warn);background:var(--warn-bg);padding:.1rem .5rem;border-radius:999px;font-weight:600}
  button{font:inherit;padding:.25rem .6rem;border-radius:6px;border:1px solid var(--border);
    background:var(--card);color:var(--accent);cursor:pointer}button:hover{border-color:var(--accent)}
  button:disabled{opacity:.5;cursor:wait}
  .link{border:none;background:none;padding:0;text-decoration:underline;text-underline-offset:2px}
  a.svc{color:var(--accent);font-weight:700;text-decoration:none}a.svc:hover{text-decoration:underline}
  svg{display:block}.spark{display:flex;align-items:center;gap:.5rem}
  dialog{border:1px solid var(--border);border-radius:10px;background:var(--card);color:var(--fg);
    padding:1.2rem;max-width:90vw;box-shadow:0 8px 30px rgba(0,0,0,.3)}
  dialog::backdrop{background:rgba(0,0,0,.4)}
</style></head><body>
  <h1>Tunnelkit</h1>
  <div class="sub">Self-hosted server tunnels · <span id="gen" class="muted"></span></div>
  <div class="summary" id="sum"></div>
  <div class="wrap"><table><thead><tr>
    <th>Client</th><th>Status</th><th>Source IP</th><th>Uptime</th><th>Conns</th>
    <th>Reconns</th><th>Latency</th><th>Traffic ↓/↑ · today</th><th>Speedtest</th>
  </tr></thead><tbody id="rows"></tbody></table></div>
  <dialog id="dlg"><div id="dlgc"></div><br>
    <button onclick="document.getElementById('dlg').close()">Close</button></dialog>
<script>
function fmtU(s){if(s==null)return '-';s=Math.floor(s);const d=Math.floor(s/86400);s%=86400;
  const h=Math.floor(s/3600);s%=3600;const m=Math.floor(s/60);
  if(d)return d+'d '+h+'h '+m+'m';if(h)return h+'h '+m+'m';return m+'m';}
function fmtR(n){if(!n)return '0';const u=['','K','M','G'];let i=0;n*=8;
  while(n>=1000&&i<3){n/=1000;i++;}return n.toFixed(i?1:0)+u[i]+'bps';}
function fmtB(n){if(!n)return '0 B';const u=['B','KB','MB','GB','TB'];let i=0;
  while(n>=1024&&i<4){n/=1024;i++;}return n.toFixed(i?1:0)+' '+u[i];}
function hostFor(n){if(n.indexOf('tk-')!==0)return '';var r=n.slice(3);var i=r.lastIndexOf('-');if(i<0)return '';var site=r.slice(0,i).replace(/[^a-z0-9]/g,'');var role=r.slice(i+1).replace(/[^a-z0-9]/g,'');if(!site||!role)return '';return 'tk'+role+site;}
function svcUrl(n){var hp=hostFor((n||'').toLowerCase());if(!hp)return '';var p=location.host.split('.');if(p.length<2)return '';return location.protocol+'//'+hp+'.'+p.slice(1).join('.');}
function spark(vals,w,h,color){
  if(!vals||!vals.length)return '<svg width="'+w+'" height="'+h+'"></svg>';
  const max=Math.max(1,...vals);const dx=w/Math.max(1,vals.length-1);
  const pts=vals.map((v,i)=>(i*dx).toFixed(1)+','+(h-(v/max)*(h-2)-1).toFixed(1)).join(' ');
  return '<svg width="'+w+'" height="'+h+'"><polyline fill="none" stroke="'+color+'" stroke-width="1.5" points="'+pts+'"/></svg>';}
async function speed(name,btn){btn.disabled=true;btn.textContent='measuring…';
  try{const r=await (await fetch('/api/speedtest?client='+encodeURIComponent(name))).json();
    btn.parentElement.querySelector('.res').textContent=r.down.toFixed(1)+'↓ '+r.up.toFixed(1)+'↑ Mbps';
  }catch(e){btn.parentElement.querySelector('.res').textContent='error';}
  btn.disabled=false;btn.textContent='Measure';tick();}
async function openTraffic(name){
  const dlg=document.getElementById('dlg'),c=document.getElementById('dlgc');
  c.innerHTML='loading…';dlg.showModal();
  try{const d=await (await fetch('/api/traffic?client='+encodeURIComponent(name))).json();
    let h='<h3>'+name+' — traffic by port</h3><table><tr><th>Port</th><th>↓ received</th><th>↑ sent</th></tr>';
    (d.ports||[]).forEach(p=>h+='<tr><td class=mono>'+p.port+'</td><td class=mono>'+fmtB(p.rx)+'</td><td class=mono>'+fmtB(p.tx)+'</td></tr>');
    if(!(d.ports||[]).length)h+='<tr><td colspan=3 class=muted>no data</td></tr>';
    h+='</table><h3>Traffic by day</h3><table><tr><th>Date</th><th>Total</th></tr>';
    (d.days||[]).forEach(x=>h+='<tr><td class=mono>'+x.date+'</td><td class=mono>'+fmtB(x.bytes)+'</td></tr>');
    if(!(d.days||[]).length)h+='<tr><td colspan=2 class=muted>no data</td></tr>';
    h+='</table>';c.innerHTML=h;
  }catch(e){c.innerHTML='error: '+e;}}
async function tick(){try{
  const d=await (await fetch('/api/status',{cache:'no-store'})).json();
  document.getElementById('gen').textContent=d.generated_at?'updated '+new Date(d.generated_at*1000).toLocaleTimeString():'';
  const cs=d.clients||[];
  document.getElementById('sum').innerHTML='<span class="pill">'+cs.length+' connected</span>'+
    '<span class="pill">'+cs.reduce((a,c)=>a+(c.active||0),0)+' active connections</span>'+
    '<span class="pill">'+fmtB(cs.reduce((a,c)=>a+(c.traffic_today||0),0))+' today</span>'+
    '<span class="pill">'+cs.reduce((a,c)=>a+(c.reconnects||0),0)+' reconnections</span>';
  document.getElementById('rows').innerHTML=cs.map(c=>{
    const rc=(c.reconnects||0)>=5?'<span class="hi">'+c.reconnects+' ⚠</span>':'<span class="mono">'+(c.reconnects||0)+'</span>';
    const h=c.hist||[];const rx=h.map(s=>s.rx),tx=h.map(s=>s.tx);
    const traf='<div class="spark"><div>'+spark(rx,60,20,'var(--rx)')+spark(tx,60,10,'var(--tx)')+'</div>'+
      '<button class="link mono" onclick="openTraffic(\''+c.name+'\')" title="view by port/day">'+fmtB(c.traffic_today)+'</button></div>';
    let sp='<span class="muted">—</span>';
    if(c.speedtest){sp='<div class="spark"><button onclick="speed(\''+c.name+'\',this)">Measure</button>'+
      '<span class="res mono muted">'+(c.last_down?c.last_down.toFixed(1)+'↓ '+c.last_up.toFixed(1)+'↑':'')+'</span>'+
      spark(c.speed_hist,50,20,'var(--accent)')+'</div>';}
    const _u=svcUrl(c.name);
    const nm=_u?'<a class="svc" href="'+_u+'" target="_blank" rel="noopener" title="open main web">'+c.name+'</a>':'<b>'+c.name+'</b>';
    return '<tr><td>'+nm+'<br><span class="mono muted" style="font-size:.75rem">'+(c.ports||[]).join(' ')+'</span></td>'+
      '<td><span class="badge up"><span class="dot"></span>ON</span></td><td class="mono">'+(c.ip||'-')+'</td>'+
      '<td class="mono">'+fmtU(c.uptime_seconds)+'</td><td class="mono">'+(c.active||0)+'</td>'+
      '<td>'+rc+'</td><td class="mono muted">'+(c.latency_ms?c.latency_ms.toFixed(0)+' ms':'-')+'</td>'+
      '<td>'+traf+'</td><td>'+sp+'</td></tr>';
  }).join('')||'<tr><td colspan="9" class="muted">no clients connected</td></tr>';
}catch(e){document.getElementById('sum').innerHTML='<span class="pill">'+e+'</span>';}}
tick();setInterval(tick,5000);
</script></body></html>`
