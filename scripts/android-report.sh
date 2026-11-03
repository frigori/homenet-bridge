#!/data/data/com.termux/files/usr/bin/bash
# Runs the Android field test in Termux and writes a report to send back.
#
#   curl -fsSLo hb-test.sh https://raw.githubusercontent.com/frigori/homenet-bridge/main/scripts/android-report.sh
#   bash hb-test.sh
#
# It collects the phone and build details, checks the upstream, runs a sharing session with
# --verbose while you test from another device, then asks a few yes/no questions. Jellyfin
# tokens in the request log are replaced with REDACTED before anything is saved.
set -u

UPSTREAM=https://jellyfin.lan.home.arpa
CA_FILE=$HOME/storage/downloads/homelab-root-ca.crt
CA_FP=29:A9:C2:AC:99:77:E9:9E:1F:A8:7B:0E:FD:A4:DA:36:DA:E9:C6:0B:3B:D0:AE:1E:06:72:C4:86:8E:1D:F0:2C
STAMP=$(date +%Y%m%d-%H%M)
REPORT=$HOME/homenet-bridge-report-$STAMP.txt
SHARE_LOG=$(mktemp)
export PATH="$HOME/go/bin:$PATH"

bold() { printf '\n\033[1m%s\033[0m\n' "$*"; }
note() { printf '%s\n' "$*" >>"$REPORT"; }
section() { printf '\n== %s ==\n' "$*" >>"$REPORT"; }
# run CMD... records the command and its output, also shown on screen.
run() {
	note "\$ $*"
	"$@" 2>&1 | redact | tee -a "$REPORT"
}
# Jellyfin puts the user's access token in stream URLs.
redact() {
	sed -E 's/((api_key|ApiKey|api-key|X-Emby-Token|X-MediaBrowser-Token|[Tt]oken)=)[^&[:space:]]+/\1REDACTED/g'
}
# ask QUESTION: a yes/no question; the answer goes into the report.
ask() {
	local a
	read -r -p "$1 [y/n] " a
	case $a in [Yy]*) a=yes ;; [Nn]*) a=no ;; *) a="(no answer) $a" ;; esac
	note "$1 $a"
}
# askText QUESTION: a free-text answer.
askText() {
	local a
	read -r -p "$1 " a
	note "$1 $a"
}

: >"$REPORT"
note "homenet-bridge Android field test, $(date)"

bold "1/5  Phone and Termux"
section "Phone"
note "Model: $(getprop ro.product.manufacturer) $(getprop ro.product.model)"
note "Android: $(getprop ro.build.version.release) (SDK $(getprop ro.build.version.sdk))"
note "Termux: ${TERMUX_VERSION:-unknown}, from ${TERMUX_APK_RELEASE:-unknown}"
tail -n 3 "$REPORT"

section "Build"
if ! command -v homenet-bridge >/dev/null; then
	echo "homenet-bridge isn't installed. Follow steps 2 and 3 of the test page first." | tee -a "$REPORT"
	exit 1
fi
run go version
run homenet-bridge version
# The module version and whether it was built with cgo (Android's own name lookup).
go version -m "$(command -v homenet-bridge)" 2>&1 | grep -E 'mod|CGO_ENABLED|GOOS|GOARCH' | tee -a "$REPORT"

bold "2/5  Certificate"
section "Certificate"
if [ -f "$CA_FILE" ]; then
	out=$(homenet-bridge trust add "$CA_FILE" 2>&1)
	printf '%s\n' "$out" | tee -a "$REPORT"
	if printf '%s' "$out" | grep -q "$CA_FP"; then
		note "Fingerprint matches."
	else
		note "FINGERPRINT DOES NOT MATCH."
		echo "The fingerprint doesn't match. Stop here and tell Mattia."
		exit 1
	fi
else
	note "No $CA_FILE (run termux-setup-storage?); using the one trusted earlier."
	tail -n 1 "$REPORT"
fi

bold "3/5  Network and upstream (Twingate must be on)"
section "Network"
ip -4 -o addr show 2>&1 | awk '{print $2, $4}' | tee -a "$REPORT"
note "resolv.conf: $(grep -h nameserver "${PREFIX:-/usr}/etc/resolv.conf" 2>/dev/null | tr '\n' ' ')"
section "Check"
start=$(date +%s)
run homenet-bridge check --upstream "$UPSTREAM"
note "(took $(($(date +%s) - start)) s)"

bold "4/5  Sharing"
ip=$(ip -4 -o addr show wlan0 2>/dev/null | awk '{sub(/\/.*/, "", $4); print $4; exit}')
if [ -n "$ip" ]; then
	read -r -p "Phone Wi-Fi address is $ip. Press Enter to use it, or type another: " other
	ip=${other:-$ip}
else
	read -r -p "Couldn't find the Wi-Fi address. Type it (Settings > Wi-Fi > your network > IP address): " ip
fi
section "Share on $ip"

cat <<EOF

Now:
  1. On the TV or other device, open Jellyfin > Add server. Use the one
     that appears by itself, or type http://$ip:8096
  2. When this screen asks "Allow? [y/N]", type y and Enter (within 25 s).
  3. Sign in, start a video, then LOCK THE PHONE for at least 5 minutes.
  4. Unlock and press CTRL + C here to stop. The script carries on after.

EOF
read -r -p "Press Enter to start sharing. " _
termux-wake-lock 2>/dev/null

# CTRL + C stops the bridge but not this script; tee -i keeps the bridge's last lines.
trap 'true' INT
homenet-bridge share --upstream "$UPSTREAM" --bind "$ip" --verbose --for 1h 2>&1 | tee -i "$SHARE_LOG"
trap - INT
termux-wake-unlock 2>/dev/null
redact <"$SHARE_LOG" >>"$REPORT"
note "Stopped at $(date +%H:%M:%S)"
rm -f "$SHARE_LOG"

# Requests during playback come every few seconds; a long gap means the phone stopped serving.
section "Request timing"
timing=$(awk '
	match($0, /^[0-9][0-9]:[0-9][0-9]:[0-9][0-9] [0-9a-f.:\[\]]+ (GET|POST|HEAD|PUT|DELETE|OPTIONS) /) {
		split(substr($0, 1, 8), t, ":"); s = t[1] * 3600 + t[2] * 60 + t[3]
		if (n++ == 0) first = substr($0, 1, 8)
		else if (s - prev > gap) { gap = s - prev; gapAt = substr($0, 1, 8) }
		prev = s; last = substr($0, 1, 8)
	}
	END {
		if (n == 0) { print "No requests logged."; exit }
		printf "%d requests from %s to %s\n", n, first, last
		printf "Longest gap between requests: %d s (ending %s)\n", gap, gapAt
	}' <"$REPORT")
printf '%s\n' "$timing" | tee -a "$REPORT"

bold "5/5  A few questions"
section "Answers"
askText "Which device and app did you use (e.g. LG TV, Jellyfin app)?"
ask "Did the server appear by itself in Add server?"
ask "Did the Allow? question appear here?"
ask "Could you sign in on the device?"
ask "Did the video play?"
ask "Was it still playing after 5 minutes with the phone locked?"
askText "If it stopped, after how long? (Enter to skip)"
askText "Wi-Fi used (home / guest / hotspot)?"
askText "Anything else?"

dest=$REPORT
if [ -d "$HOME/storage/downloads" ]; then
	cp "$REPORT" "$HOME/storage/downloads/" && dest=$HOME/storage/downloads/$(basename "$REPORT")
fi
bold "Done. Report saved to:"
echo "  $dest"
echo "Send it to Mattia from any chat or mail app."
