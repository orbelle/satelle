session=<session>
sbin=$(command -v satelle)
dbin=<the path in the [...] of the "installed" line of satelle service status>
bk="$(satelle runtime path --session "$session")/backups/bin/$(date -u +%Y%m%dT%H%M%SZ)"; mkdir -p "$bk"; cp -p "$sbin" "$dbin" "$bk/"
printf 'restore: install -m 0755 %s %s && install -m 0755 %s %s && satelle service restart --session <session>\n' "$bk/satelle" "$sbin" "$bk/satelled" "$dbin"
satelle update --version <ver> --session "$session"
"$sbin" update --version <ver> --session "$session"
satelle service restart --session "$session"
