package api

import "bytes"

// consoleInkCSS recolors the upstream management console. The asset file stays
// the upstream copy. These variables are the ones that console already reads.
const consoleInkCSS = `:root,[data-theme=dark],[data-theme=white]{` +
	`--bg-secondary:#0b1220;--bg-primary:#070b14;--bg-tertiary:#121a2b;--bg-hover:#182338;` +
	`--bg-quinary:#0a101c;--bg-error-light:#c657462e;--floating-surface:#10192b;` +
	`--floating-shadow:0 14px 30px #0008;--text-primary:#e7eefc;--text-secondary:#a9b7d0;` +
	`--text-tertiary:#7e8da8;--text-quaternary:#5c6b84;--text-muted:var(--text-tertiary);` +
	`--border-color:#243044;--border-secondary:var(--border-color);--border-primary:#31425e;` +
	`--border-hover:#3d5274;--primary-color:#8eb0ff;--primary-hover:#b3c7ff;` +
	`--primary-active:#d5e1ff;--primary-contrast:#07101f;--success-color:#3dcea0;` +
	`--quota-medium-color:#e2c16a;--warning-color:#e07a68;--error-color:#e07a68;` +
	`--danger-color:var(--error-color);--info-color:var(--primary-color);` +
	`--warning-bg:#e07a6830;--warning-border:#e07a6866;--warning-text:#f3c2b8;` +
	`--success-badge-bg:#064e3b55;--success-badge-text:#8eebc8;--success-badge-border:#1f9d78;` +
	`--failure-badge-bg:#e07a6833;--failure-badge-text:#f3c2b8;--failure-badge-border:#e07a6880;` +
	`--count-badge-bg:#8eb0ff33;--count-badge-text:var(--primary-active);` +
	`--shadow:0 1px 3px #0006;--shadow-lg:0 10px 18px #0008;--radius-md:8px;` +
	`--amber-color:#e2c16a;--amber-text:#f0d48a;` +
	`--muted-bg:var(--bg-tertiary);--muted-foreground:var(--text-secondary);--accent-bg:var(--bg-tertiary);` +
	`--glass-bg:#070b14;--glass-bg-secondary:#0b1220;--glass-border:#243044;` +
	`--viz-success:#3dcea0;--viz-failure:#e07a68}` +
	`#fleet-native-nav{margin:8px 10px 16px;padding:8px 8px 10px;border:1px solid var(--border-color);border-radius:12px;background:var(--bg-secondary)}` +
	`#fleet-native-nav .fleet-kicker{font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--text-tertiary);padding:4px 8px 6px}` +
	`#fleet-native-nav a{display:block;padding:7px 8px;border-radius:8px;color:var(--text-primary);text-decoration:none}` +
	`#fleet-native-nav a:hover{background:var(--bg-hover)}`

const consoleInkScript = `(function(){
function decodeKey(){
  var raw=null;
  try{raw=localStorage.getItem("managementKey");}catch(e){return "";}
  if(!raw)return "";
  var prefix="enc::v1::";
  var text=raw;
  if(raw.indexOf(prefix)===0){
    try{
      var secret="cli-proxy-api-webui::secure-storage|"+(location.host||"")+"|"+(navigator.userAgent||"");
      var bytes=atob(raw.slice(prefix.length));
      var out="";
      for(var i=0;i<bytes.length;i++) out+=String.fromCharCode(bytes.charCodeAt(i)^secret.charCodeAt(i%secret.length));
      text=out;
    }catch(e){return "";}
  }
  try{var parsed=JSON.parse(text);return typeof parsed==="string"?parsed:"";}catch(e){return "";}
}
function remember(key){
  if(!key)return;
  try{sessionStorage.setItem("fleet.hub.key", key);}catch(e){}
}
remember(decodeKey());
if(window.fetch){
  var orig=window.fetch;
  window.fetch=function(input, init){
    try{
      var headers=init&&init.headers;
      var auth="";
      if(headers){
        if(typeof headers.get==="function") auth=headers.get("Authorization")||headers.get("authorization")||"";
        else auth=headers.Authorization||headers.authorization||"";
      }
      if(typeof auth==="string" && auth.slice(0,7).toLowerCase()==="bearer ") remember(auth.slice(7).trim());
    }catch(e){}
    return orig.apply(this, arguments);
  };
}
function place(){
  var links=[].slice.call(document.querySelectorAll('a[href^="/plugin-pages/fleet/"]')).filter(function(a){return !a.closest("#fleet-native-nav");});
  if(!links.length) return;
  var drawer=null;
  links.forEach(function(a){
    var n=a;
    for(var i=0;i<10 && n;i++){
      n=n.parentElement;
      if(!n) break;
      var text=n.innerText||"";
      if(text.indexOf("Fleet Hub")!==-1 && text.indexOf("Dashboard")===-1 && text.indexOf("AI Providers")===-1) drawer=n;
    }
    if(/router/i.test(a.textContent||"")){
      var row=a.closest("li")||a.parentElement||a;
      row.style.display="none";
    }
  });
  if(drawer) drawer.setAttribute("hidden","");
  var aside=document.querySelector("aside")||document.querySelector("nav");
  if(!aside) return;
  var want=links.filter(function(a){return !/router/i.test(a.textContent||"");});
  var sig=want.map(function(a){return (a.getAttribute("href")||"")+"|"+(a.textContent||"").trim();}).join(",");
  var box=document.getElementById("fleet-native-nav");
  if(!box){
    box=document.createElement("div");
    box.id="fleet-native-nav";
    var pluginLabel=[].slice.call(aside.querySelectorAll("div,span,p,button")).find(function(el){
      return /^plugins$/i.test((el.textContent||"").trim());
    });
    if(pluginLabel && pluginLabel.parentElement && pluginLabel.parentElement.parentElement){
      pluginLabel.parentElement.parentElement.insertBefore(box, pluginLabel.parentElement);
    }else aside.appendChild(box);
  }
  if(box.getAttribute("data-sig")===sig) return;
  box.setAttribute("data-sig", sig);
  box.textContent="";
  var kicker=document.createElement("div");
  kicker.className="fleet-kicker";
  kicker.textContent="Fleet";
  box.appendChild(kicker);
  want.forEach(function(a){
    var link=document.createElement("a");
    link.href=a.getAttribute("href");
    link.textContent=(a.textContent||"").trim();
    box.appendChild(link);
  });
}
var scheduled=false;
function schedule(){
  if(scheduled) return;
  scheduled=true;
  requestAnimationFrame(function(){scheduled=false; place();});
}
if(document.documentElement){
  new MutationObserver(schedule).observe(document.documentElement,{childList:true,subtree:true});
}
document.documentElement.setAttribute("data-theme","dark");
schedule();
})();`

// InjectConsoleInk appends the Fleet palette and sidebar script to a management
// document. The original bytes stay intact ahead of the injection.
func InjectConsoleInk(html []byte) []byte {
	if len(html) == 0 {
		return html
	}
	block := []byte("<style id=\"fleet-ink\">" + consoleInkCSS + "</style><script>" + consoleInkScript + "</script>")
	if i := bytes.LastIndex(html, []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(html)+len(block))
		out = append(out, html[:i]...)
		out = append(out, block...)
		out = append(out, html[i:]...)
		return out
	}
	return append(append([]byte{}, html...), block...)
}
