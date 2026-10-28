module("luci.controller.openbox_compat", package.seeall)

-- Old Lua-only LuCI cannot load an action of type "view". Keep the existing
-- Services entry useful by opening the same authenticated Open-Box panel.
function index()

	entry({"admin", "services", "openbox"}, call("redirect_panel"), _("Open-Box"), 30).dependent = false
end

function redirect_panel()
	local http = require "luci.http"
	local fs = require "nixio.fs"
	local raw = http.getenv("HTTP_HOST") or ""
	local host = raw:match("^(%[[%x:%.]+%]):?%d*$") or raw:match("^([%w%.%-]+):?%d*$") or "127.0.0.1"
	local port = tonumber((fs.readfile("/opt/open-box/data/panel-port") or ""):match("^%s*(%d+)%s*$")) or 2026
	if port < 1024 or port > 65535 then port = 2026 end
	http.redirect("http://" .. host .. ":" .. port .. "/")
end
