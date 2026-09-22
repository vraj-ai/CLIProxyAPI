package main

import (
	_ "embed"
	"strings"
)

//go:embed theme.css
var themeCSS string

//go:embed hub.html
var hubPageHTML string

//go:embed savings.html
var savingsPageHTML string

//go:embed router.html
var routerPageHTML string

//go:embed keys.html
var keysPageHTML string

const fleetKeyJS = `function fleetConsoleKey(storageKey){
  try{var own=sessionStorage.getItem(storageKey);if(own)return own;}catch(e){}
  var decoded=fleetDecodeManagementKey();
  if(!decoded)return "";
  try{sessionStorage.setItem(storageKey, decoded);}catch(e){}
  return decoded;
}
function fleetDecodeManagementKey(){
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
if(window.top!==window.self) document.body.classList.add("framed");
function fleetWatchKey(storageKey, input, reload){
  if(input.value) return;
  var tries=0;
  var timer=setInterval(function(){
    var found=fleetConsoleKey(storageKey);
    tries++;
    if(found){input.value=found;clearInterval(timer);if(reload)reload();}
    else if(tries>20) clearInterval(timer);
  }, 250);
}
`

func withTheme(raw string) string {
	raw = strings.Replace(raw, "/*THEME*/", themeCSS, 1)
	return strings.Replace(raw, "/*KEY*/", fleetKeyJS, 1)
}
