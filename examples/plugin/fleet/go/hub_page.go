package main

func legacyPage(path string) string {
	destination := "/management.html#/fleet"
	switch path {
	case "/keys":
		destination = "/management.html#/fleet/access"
	case "/savings":
		destination = "/management.html#/fleet/activity"
	}

	legacyHashRoutes := `{"/keys":"/management.html#/fleet/access","/models":"/management.html#/fleet/access?tab=models","/activity":"/management.html#/fleet/activity","/providers":"/management.html#/ai-providers","/console":"/management.html#/config"}`
	if path == "/hub" || path == "/" {
		return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="0;url=/management.html#/fleet"><title>Fleet</title></head><body><a href="/management.html#/fleet">Open Fleet</a><script>const routes=` + legacyHashRoutes + `;const target=routes[location.hash.replace(/^#/,"")]||"/management.html#/fleet";location.replace(target);</script></body></html>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="0;url=` + destination + `"><title>Fleet</title></head><body><a href="` + destination + `">Open Fleet</a><script>location.replace("` + destination + `");</script></body></html>`
}
