package api

import "bytes"

// consoleInk is appended to the served management.html. The upstream asset is
// never edited: the dark blue token sheet and the Fleet rail are injected at
// serve time so the 3h auto-update cannot clobber them.
const consoleInk = `<style id="fleet-console-ink">
:root{
  --fleet-bg:#05070c;--fleet-panel:#0a0f18;--fleet-well:#101826;
  --fleet-line:#1b2a40;--fleet-ink:#e9eef7;--fleet-lamp:#4d9fff;
}
html,body{background:var(--fleet-bg)!important;color:var(--fleet-ink);}
#fleet-rail{position:fixed;inset:0 auto 0 0;z-index:2147483000;display:flex;
  flex-direction:column;gap:2px;padding:14px 10px;background:var(--fleet-panel);
  border-right:1px solid var(--fleet-line);font:13px/1.4 system-ui,sans-serif;}
#fleet-rail a{color:var(--fleet-ink);opacity:.75;text-decoration:none;padding:6px 10px;border-radius:8px;}
#fleet-rail a:hover{background:var(--fleet-well);opacity:1;}
#fleet-rail a.rail-here{background:var(--fleet-lamp);color:var(--fleet-bg);opacity:1;}
</style>
<script id="fleet-console-rail">
(function(){
  var links=[["Hub","/v0/resource/plugins/fleet/hub"],["Keys","/v0/resource/plugins/fleet/keys"],["Savings","/v0/resource/plugins/fleet/savings"]];
  var rail=document.createElement("nav");rail.id="fleet-rail";rail.setAttribute("aria-label","Fleet");
  links.forEach(function(pair){
    var a=document.createElement("a");a.href=pair[1];a.textContent=pair[0];
    if(location.pathname.indexOf(pair[1])!==-1)a.className="rail-here";
    rail.appendChild(a);
  });
  function sweep(){
    var nodes=document.querySelectorAll('a[href*="/plugins"],[role="menuitem"],li');
    for(var i=0;i<nodes.length;i++){
      var el=nodes[i],t=(el.textContent||"").trim(),h=el.getAttribute("href")||"";
      if(h.indexOf("/v0/resource/plugins/fleet/router")!==-1||t==="Fleet Router"||t==="Router"||
         h.indexOf("/v0/resource/plugins")!==-1&&el.tagName==="A"&&t.indexOf("Fleet")===0&&t!=="Fleet Hub"&&t!=="Fleet Keys"){
        el.style.display="none";
      }
    }
  }
  function mount(){document.body.appendChild(rail);sweep();}
  if(document.readyState==="loading"){document.addEventListener("DOMContentLoaded",mount);}else{mount();}
  if(window.MutationObserver){new MutationObserver(sweep).observe(document.documentElement,{childList:true,subtree:true});}
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
