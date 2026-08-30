package frr

import (
	"os"
	"path/filepath"
)

const daemonsConfig = `zebra=yes
bgpd=yes
bfdd=yes
vtysh_enable=yes
zebra_options=" -A 127.0.0.1 -s 90000000"
bgpd_options=" -A 127.0.0.1"
bfdd_options=" -A 127.0.0.1"
`

const vtyshConfig = `service integrated-vtysh-config
`

const reloadScript = `#!/bin/sh
FRR_DIR=${FRR_DIR:-/etc/frr}
RELOAD_PY=/usr/lib/frr/frr-reload.py
[ -f "$RELOAD_PY" ] || RELOAD_PY=$(command -v frr-reload.py)
REDACT='s/password .*/password (redacted)/'
SUMMARY="$FRR_DIR/.fabric-frr-bgp-summary.json"
ROUTES="$FRR_DIR/.fabric-frr-bgp-routes.json"
snapshot() {
  if [ ! -S /var/run/frr/bgpd.vty ]; then
    rm -f "$SUMMARY" "$ROUTES"
    return
  fi
  vtysh -c 'show bgp summary json' > "$SUMMARY.tmp" 2>/dev/null && mv "$SUMMARY.tmp" "$SUMMARY"
  vtysh -c 'show bgp ipv4 unicast json' > "$ROUTES.tmp" 2>/dev/null && mv "$ROUTES.tmp" "$ROUTES"
}
tick=0
while true; do
  sleep 1
  tick=$((tick + 1))
  [ $((tick % 5)) -eq 0 ] && snapshot
  [ -f "$FRR_DIR/.fabric-frr-apply" ] || continue
  want=$(cat "$FRR_DIR/.fabric-frr-apply")
  have=$(cat "$FRR_DIR/.fabric-frr-applied" 2>/dev/null)
  [ "$want" = "$have" ] && continue
  if ! python3 "$RELOAD_PY" --test "$FRR_DIR/frr.conf.desired" >/tmp/frr-test.log 2>&1; then
    { printf 'error %s test\n' "$want"; sed "$REDACT" /tmp/frr-test.log | tail -20; } > "$FRR_DIR/.fabric-frr-result"
    sleep 5
    continue
  fi
  if python3 "$RELOAD_PY" --reload --overwrite "$FRR_DIR/frr.conf.desired" >/tmp/frr-reload.log 2>&1; then
    printf '%s\n' "$want" > "$FRR_DIR/.fabric-frr-applied"
    printf 'ok %s\n' "$want" > "$FRR_DIR/.fabric-frr-result"
    continue
  fi
  { printf 'error %s reload\n' "$want"; sed "$REDACT" /tmp/frr-reload.log | tail -20; } > "$FRR_DIR/.fabric-frr-result"
  sleep 5
done
`

func InitFrrDir(frrDir, nodeName string) error {
	if err := os.MkdirAll(frrDir, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"daemons":          daemonsConfig,
		"vtysh.conf":       vtyshConfig,
		"fabric-reload.sh": reloadScript,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(frrDir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}

	frrConf := filepath.Join(frrDir, "frr.conf")
	if _, err := os.Stat(frrConf); os.IsNotExist(err) {
		initial := Render(RenderInput{NodeName: nodeName})
		return os.WriteFile(frrConf, []byte(initial), 0o600)
	}
	return nil
}
