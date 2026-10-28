#!/bin/sh
# Sourced by install/update and the panel launcher. JS views also exist on some
# Lua-based LuCI releases, so test the JS runtime, not just the ucode directory.
openbox_install_luci_compat() {
	_ob_lc_root="$1"
	_ob_lc_sys="${2:-}"
	_ob_lc_dst="$_ob_lc_sys/usr/lib/lua/luci/controller/openbox_compat.lua"
	_ob_lc_menu="$_ob_lc_sys/usr/share/luci/menu.d/luci-app-openbox.json"
	OPENBOX_LUCI_COMPAT_CHANGED=0
	if { [ -f "$_ob_lc_sys/usr/lib/lua/luci/dispatcher.lua" ] || [ -f "$_ob_lc_sys/usr/lib/lua/luci/dispatcher.luac" ]; } &&
		[ ! -f "$_ob_lc_sys/www/luci-static/resources/luci.js" ] && [ ! -f "$_ob_lc_sys/www/luci-static/luci.js" ] &&
		[ ! -d "$_ob_lc_sys/usr/share/ucode/luci" ]; then
		_ob_lc_src="$_ob_lc_root/openwrt/luci/root/usr/lib/lua/luci/controller/openbox_compat.lua"
		if [ ! -f "$_ob_lc_dst" ] || ! cmp -s "$_ob_lc_src" "$_ob_lc_dst"; then
			mkdir -p "${_ob_lc_dst%/*}" && cp "$_ob_lc_src" "$_ob_lc_dst" && chmod 644 "$_ob_lc_dst" || return 1
			OPENBOX_LUCI_COMPAT_CHANGED=1
		fi
		# Do not leave an unsupported JSON "view" action competing with the controller.
		if [ -f "$_ob_lc_menu" ]; then
			rm -f "$_ob_lc_menu" || return 1
			OPENBOX_LUCI_COMPAT_CHANGED=1
		fi
	else
		if [ -f "$_ob_lc_dst" ]; then
			rm -f "$_ob_lc_dst" || return 1
			OPENBOX_LUCI_COMPAT_CHANGED=1
		fi
		_ob_lc_src="$_ob_lc_root/openwrt/luci/root/usr/share/luci/menu.d/luci-app-openbox.json"
		if [ ! -f "$_ob_lc_menu" ] || ! cmp -s "$_ob_lc_src" "$_ob_lc_menu"; then
			mkdir -p "${_ob_lc_menu%/*}" && cp "$_ob_lc_src" "$_ob_lc_menu" && chmod 644 "$_ob_lc_menu" || return 1
			OPENBOX_LUCI_COMPAT_CHANGED=1
		fi
	fi
	return 0
}
