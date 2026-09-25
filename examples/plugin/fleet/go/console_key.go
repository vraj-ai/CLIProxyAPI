package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// The console stores its remembered management key in localStorage under the
// browser-side obfuscation scheme below. The constants are the single source
// of truth for both the Go oracle and the served keyring script.
const (
	consoleKeyPrefix = "enc::v1::"
	consoleKeySeed   = "cli-proxy-api-webui::secure-storage"
)

func consoleKeyMaterial(host, userAgent string) []byte {
	return []byte(consoleKeySeed + "|" + host + "|" + userAgent)
}

// decodeConsoleStored mirrors the console's browser-side decode: an
// "enc::v1::" value is base64 then xor against the seed material; the
// plaintext is a JSON string. Non-prefixed values pass through after a JSON
// string parse attempt, matching the console's plaintext-key migration.
// Prefixed values that do not decode to a JSON string yield "".
func decodeConsoleStored(raw, host, userAgent string) string {
	if raw == "" {
		return ""
	}
	val := raw
	prefixed := strings.HasPrefix(val, consoleKeyPrefix)
	if prefixed {
		bin, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(val, consoleKeyPrefix))
		if err != nil {
			return ""
		}
		key := consoleKeyMaterial(host, userAgent)
		if len(key) == 0 {
			return ""
		}
		out := make([]byte, len(bin))
		for i := range bin {
			out[i] = bin[i] ^ key[i%len(key)]
		}
		val = string(out)
	}
	var parsed any
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		if prefixed {
			return ""
		}
		return raw
	}
	if s, ok := parsed.(string); ok {
		return s
	}
	if prefixed {
		return ""
	}
	return raw
}

// consoleKeyJS is the browser twin of decodeConsoleStored. It fills the
// management key field from console storage and never writes the key to the
// console, the DOM as text, or the network.
func consoleKeyJS() string {
	return `(function(){
function mat(){return new TextEncoder().encode("` + consoleKeySeed + `|"+location.host+"|"+navigator.userAgent);}
window.consoleStoredKey=function(){
  try{
    var raw=localStorage.getItem("managementKey");
    if(!raw)return"";
    var val=raw,prefixed=false;
    if(val.indexOf("` + consoleKeyPrefix + `")===0){
      prefixed=true;
      var k=mat(),bin=atob(val.slice(` + strconv.Itoa(len(consoleKeyPrefix)) + `));
      var buf=new Uint8Array(bin.length);
      for(var i=0;i<bin.length;i++)buf[i]=bin.charCodeAt(i)^k[i%k.length];
      val=new TextDecoder().decode(buf);
    }
    try{
      var p=JSON.parse(val);
      return typeof p==="string"?p:(prefixed?"":raw);
    }catch(e){return prefixed?"":raw;}
  }catch(e){return"";}
};
})();`
}
