package api

import "bytes"

// consoleInk is appended to the served management.html. The upstream asset is
// never edited: the Fleet chrome (nav rail, status pill, motion tokens) is
// injected at serve time so the 3h auto-update cannot clobber it.
const consoleInk = `<style id="fleet-console-ink">
:root{
  --fleet-bg:#04060b;--fleet-panel:rgba(13,19,32,.92);--fleet-well:rgba(20,30,50,.7);
  --fleet-line:rgba(120,165,255,.16);--fleet-ink:#edf2fb;--fleet-muted:#93a7c4;
  --fleet-lamp:#5ea2ff;--fleet-lamp-hot:#9cc7ff;--fleet-teal:#4de3c2;--fleet-violet:#a78bff;
  --fleet-bad:#ff6b60;--fleet-ok:#4de3a4;
  --fleet-ease:cubic-bezier(.22,1,.36,1);
}
html,body{background:var(--fleet-bg)!important;color:var(--fleet-ink);}
body{padding-left:196px!important;transition:padding .3s var(--fleet-ease);}
body::before{content:"";position:fixed;inset:0;z-index:0;pointer-events:none;
  background:
    radial-gradient(560px 380px at -8% -6%, rgba(94,162,255,.20), transparent 65%),
    radial-gradient(480px 340px at 108% 10%, rgba(167,139,255,.14), transparent 65%),
    linear-gradient(rgba(120,165,255,.045) 1px, transparent 1px),
    linear-gradient(90deg, rgba(120,165,255,.045) 1px, transparent 1px);
  background-size:auto,auto,44px 44px,44px 44px;}
#fleet-rail{position:fixed;inset:0 auto 0 0;z-index:2147483000;width:196px;display:flex;
  flex-direction:column;gap:2px;padding:16px 12px;background:var(--fleet-panel);
  border-right:1px solid var(--fleet-line);font:14px/1.45 "Figtree",system-ui,sans-serif;
  backdrop-filter:blur(18px);-webkit-backdrop-filter:blur(18px);
  animation:fleet-rail-in .45s var(--fleet-ease) both;}
@keyframes fleet-rail-in{from{opacity:0;transform:translateX(-14px);}to{opacity:1;transform:none;}}
#fleet-rail .rail-brand{display:flex;gap:10px;align-items:center;padding:4px 10px 12px;border-bottom:1px solid var(--fleet-line);margin-bottom:10px;}
#fleet-rail .rail-logo{width:32px;height:32px;border-radius:10px;flex:none;position:relative;
  background:conic-gradient(from 210deg,var(--fleet-lamp),var(--fleet-violet),var(--fleet-teal),var(--fleet-lamp));
  box-shadow:0 0 18px rgba(94,162,255,.4);}
#fleet-rail .rail-logo::after{content:"";position:absolute;inset:3px;border-radius:7px;background:#0a1120;}
#fleet-rail .rail-word{font-weight:800;font-size:18px;letter-spacing:-.02em;color:var(--fleet-ink);}
#fleet-rail .rail-word em{font-style:normal;color:var(--fleet-lamp-hot);}
#fleet-rail .rail-sub{font-size:11px;color:var(--fleet-muted);}
#fleet-rail a.rail-link{color:var(--fleet-ink);opacity:.72;text-decoration:none;padding:9px 12px;border-radius:11px;
  border:1px solid transparent;transition:all .22s var(--fleet-ease);display:flex;justify-content:space-between;align-items:center;gap:8px;}
#fleet-rail a.rail-link:hover{background:rgba(94,162,255,.12);opacity:1;transform:translateX(2px);text-decoration:none;}
#fleet-rail a.rail-link.rail-here{background:linear-gradient(135deg,var(--fleet-lamp-hot),var(--fleet-lamp));color:#06101f;opacity:1;font-weight:700;
  box-shadow:0 4px 18px rgba(94,162,255,.35);}
#fleet-rail a.rail-link .k{font:600 10px ui-monospace,monospace;opacity:.65;}
#fleet-rail .rail-foot{margin-top:auto;padding:10px;border-top:1px solid var(--fleet-line);font-size:11px;color:var(--fleet-muted);}
#fleet-status{display:flex;gap:7px;align-items:center;font:600 11px ui-monospace,monospace;}
#fleet-status i{width:8px;height:8px;border-radius:50%;background:var(--fleet-muted);}
#fleet-status.ok i{background:var(--fleet-ok);box-shadow:0 0 10px var(--fleet-ok);animation:fleet-pulse 1.6s infinite;}
#fleet-status.bad i{background:var(--fleet-bad);box-shadow:0 0 10px var(--fleet-bad);animation:fleet-pulse 1.6s infinite;}
@keyframes fleet-pulse{0%,100%{opacity:1;}50%{opacity:.45;}}
@media(max-width:760px){body{padding-left:0!important;}#fleet-rail{position:static;width:auto;flex-direction:row;flex-wrap:wrap;align-items:center;}}
@media(prefers-reduced-motion:reduce){#fleet-rail{animation:none;}#fleet-status.ok i,#fleet-status.bad i{animation:none;}}
</style>
<script id="fleet-console-rail">
(function(){
  // ponytail: suppresses only the injected rail when Console runs embedded
  // (already inside Fleet); upgrade to a signed postMessage handshake if
  // Fleet ever embeds Console with a JS bridge.
  function embedded(){
    try {
      if (window.top !== window.self) return true;
    } catch (e) { return true; }
    return /(?:^|[?&])fleet-embed=1(?:&|$)/.test(location.search || "");
  }
  if (embedded()) return;
  var tabs=[["Overview","/v0/resource/plugins/fleet/hub#/overview","1"],["Providers","/v0/resource/plugins/fleet/hub#/providers","2"],["Models","/v0/resource/plugins/fleet/hub#/models","3"],["Keys","/v0/resource/plugins/fleet/hub#/keys","4"],["Activity","/v0/resource/plugins/fleet/hub#/activity","5"],["Console","/v0/resource/plugins/fleet/hub#/console","6"]];
  var rail=document.getElementById("fleet-rail");
  if(!rail){
    rail=document.createElement("nav");rail.id="fleet-rail";rail.setAttribute("aria-label","Fleet");
    rail.innerHTML='<div class="rail-brand"><div class="rail-logo"></div><div><div class="rail-word">Fleet<em>.</em></div><div class="rail-sub">local proxy switchboard</div></div></div>';
    tabs.forEach(function(t){
      var a=document.createElement("a");a.href=t[1];a.className="rail-link";a.dataset.tab=t[0];
      var label=document.createElement("span");label.textContent=t[0];
      var k=document.createElement("span");k.className="k";k.textContent=t[2];
      a.appendChild(label);a.appendChild(k);
      if(location.pathname.indexOf("/v0/resource/plugins/fleet")!==-1)a.classList.add("rail-here");
      rail.appendChild(a);
    });
    var foot=document.createElement("div");foot.className="rail-foot";
    foot.innerHTML='<div id="fleet-status"><i></i><b>probing…</b></div><div style="margin-top:6px">canonical · 127.0.0.1:8317/v1</div>';
    rail.appendChild(foot);
  }
  function here(){
    var hs=(location.hash||"").replace(/^#\/?/,"");
    var links=rail.querySelectorAll("a.rail-link");
    for(var i=0;i<links.length;i++){
      var href=links[i].getAttribute("href")||"";
      var onFleet=location.pathname.indexOf("/v0/resource/plugins/fleet")!==-1;
      links[i].classList.toggle("rail-here", onFleet ? href.indexOf("#/"+hs)!==-1 || (hs===""&&href.indexOf("overview")!==-1) : links[i].dataset.tab==="Console" && location.pathname.indexOf("management.html")!==-1);
    }
  }
  function probe(){
    var st=document.querySelector("#fleet-status"),b=st&&st.querySelector("b");
    if(!b)return;
    fetch("/healthz",{cache:"no-store"}).then(function(r){
      st.className=r.ok?"ok":"bad";b.textContent=r.ok?"proxy live":"proxy down";
    }).catch(function(){st.className="bad";b.textContent="proxy down";});
  }
  function mount(){if(!document.getElementById("fleet-rail"))document.body.appendChild(rail);here();probe();setInterval(probe,15000);}
  if(document.readyState==="loading"){document.addEventListener("DOMContentLoaded",mount);}else{mount();}
  window.addEventListener("hashchange",here);
  if(window.MutationObserver){new MutationObserver(here).observe(document.documentElement,{childList:true,subtree:true});}
})();
</script>`

// injectConsoleInk returns the management.html body with the Fleet ink and
// rail appended. The original body is preserved verbatim: injection is
// additive so an operator diffing the served document against the upstream
// asset sees only this suffix.
func injectConsoleInk(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(consoleInk)
	}
	if idx := bytes.LastIndex(raw, []byte("</body>")); idx >= 0 {
		var out bytes.Buffer
		out.Grow(len(raw) + len(consoleInk))
		out.Write(raw[:idx])
		out.WriteString(consoleInk)
		out.Write(raw[idx:])
		return out.Bytes()
	}
	out := make([]byte, 0, len(raw)+len(consoleInk))
	out = append(out, raw...)
	out = append(out, consoleInk...)
	return out
}
