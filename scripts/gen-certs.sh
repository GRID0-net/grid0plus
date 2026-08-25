#!/usr/bin/env bash
#
# Generate the local certificate authority and the TLS certificate the nginx
# edge presents to the consoles.
#
# NOTHING FROM NINTENDO IS USED OR REPRODUCED HERE. This creates your own CA and
# your own self-signed certificates. It cannot and does not produce anything a
# real Nintendo server would accept.
#
# # Why this exists at all, given the console does not check the certificate
#
# A retail Switch will not let you add a CA to the trust store its `ssl`
# sysmodule uses, so the usual "install our root certificate" step is not
# available. What the community does instead — and what Nextendo's Prelude
# installs — is an Atmosphere exefs patch (`disable_ca_verification`) that stops
# the sysmodule validating the chain at all. See switch/README.md.
#
# So on the console the certificate's contents do not matter. They matter for
# everything else: curl during bring-up, an emulator, a browser, and any future
# client that is not patched. Getting the subject alternative names right costs
# nothing and saves an afternoon later.
#
# Usage:
#   scripts/gen-certs.sh [-c config/config.yaml] [-d certs] [-y years]

set -euo pipefail

CONFIG="${SWITCHNET_CONFIG:-}"
OUT_DIR="certs"
YEARS=10

while getopts "c:d:y:h" opt; do
    case "$opt" in
        c) CONFIG="$OPTARG" ;;
        d) OUT_DIR="$OPTARG" ;;
        y) YEARS="$OPTARG" ;;
        h)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) exit 2 ;;
    esac
done

command -v openssl >/dev/null || { echo "gen-certs: openssl is not installed" >&2; exit 1; }
command -v go >/dev/null || { echo "gen-certs: go is not installed" >&2; exit 1; }

DAYS=$((YEARS * 365))
mkdir -p "$OUT_DIR"
chmod 700 "$OUT_DIR"

# The SAN list comes from internal/nintendo, the same list that generates the
# console hosts file and the nginx routing map. Reading it from there rather
# than repeating it here is the whole point: a hostname added in one place and
# forgotten in another produces a certificate error on a console and no clue
# where it came from.
echo "gen-certs: reading the hostname list from internal/nintendo"
SAN_ARGS=""
SAN_COUNT=0
while read -r host; do
    [ -n "$host" ] || continue
    SAN_ARGS="${SAN_ARGS:+$SAN_ARGS,}DNS:$host"
    SAN_COUNT=$((SAN_COUNT + 1))
done < <(go run ./cmd/switchnetctl ${CONFIG:+-config "$CONFIG"} sans)

if [ "$SAN_COUNT" -eq 0 ]; then
    echo "gen-certs: the hostname list is empty; nothing to put in the certificate" >&2
    exit 1
fi
echo "gen-certs: $SAN_COUNT hostnames"

# ---------------------------------------------------------------------------
# The certificate authority
# ---------------------------------------------------------------------------
if [ -f "$OUT_DIR/rootCA.key" ]; then
    echo "gen-certs: reusing the existing CA at $OUT_DIR/rootCA.key"
    echo "           (delete it to start over -- every certificate signed by it"
    echo "            stops being trusted by anything that trusted the old one)"
else
    echo "gen-certs: creating the CA"
    openssl genrsa -out "$OUT_DIR/rootCA.key" 4096 2>/dev/null
    chmod 600 "$OUT_DIR/rootCA.key"

    openssl req -x509 -new -nodes \
        -key "$OUT_DIR/rootCA.key" \
        -sha256 -days "$DAYS" \
        -out "$OUT_DIR/rootCA.pem" \
        -subj "/CN=SwitchNet Local CA/O=SwitchNet" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign"
fi

# ---------------------------------------------------------------------------
# The edge certificate
# ---------------------------------------------------------------------------
echo "gen-certs: creating the edge certificate"

openssl genrsa -out "$OUT_DIR/edge.key" 2048 2>/dev/null
chmod 600 "$OUT_DIR/edge.key"

# A config file rather than -addext, because the SAN list is long enough that
# quoting it on a command line is a source of silent truncation.
cat > "$OUT_DIR/edge.cnf" <<EOF
[req]
distinguished_name = dn
req_extensions     = ext
prompt             = no

[dn]
CN = ${SAN_ARGS#DNS:}
O  = SwitchNet

[ext]
basicConstraints = CA:FALSE
keyUsage         = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName   = ${SAN_ARGS}
EOF

# The CN is only the first hostname, and is ignored by every modern client in
# favour of the SANs. It is set so `openssl x509 -text` is readable.
sed -i.bak "s|^CN = .*|CN = $(echo "$SAN_ARGS" | cut -d, -f1 | sed 's/^DNS://')|" "$OUT_DIR/edge.cnf"
rm -f "$OUT_DIR/edge.cnf.bak"

openssl req -new -key "$OUT_DIR/edge.key" -out "$OUT_DIR/edge.csr" \
    -config "$OUT_DIR/edge.cnf"

openssl x509 -req \
    -in "$OUT_DIR/edge.csr" \
    -CA "$OUT_DIR/rootCA.pem" -CAkey "$OUT_DIR/rootCA.key" \
    -CAcreateserial \
    -out "$OUT_DIR/edge.crt" \
    -days "$DAYS" -sha256 \
    -extfile "$OUT_DIR/edge.cnf" -extensions ext 2>/dev/null

rm -f "$OUT_DIR/edge.csr"

# ---------------------------------------------------------------------------
# Verify, rather than assume
# ---------------------------------------------------------------------------
echo "gen-certs: verifying"
openssl verify -CAfile "$OUT_DIR/rootCA.pem" "$OUT_DIR/edge.crt" >/dev/null

# Spot-check that a hostname the console cannot do without really is in there. A
# certificate that verifies but is missing a SAN fails at handshake time on the
# console, with nothing in any log to say which name was missing.
for required in \
    dauth-lp1.ndas.srv.nintendo.net \
    e0d67c509fb203858ebcb2fe3f88c2aa.baas.nintendo.com
do
    if ! openssl x509 -in "$OUT_DIR/edge.crt" -noout -text | grep -q "DNS:$required"; then
        echo "gen-certs: $required is missing from the certificate SANs" >&2
        exit 1
    fi
done

# Copy the CA where the Switch-side instructions expect it. It is not installed
# on a retail console (see the note at the top) but it is what an emulator, a
# browser or a development machine needs.
mkdir -p switch/ca
cp "$OUT_DIR/rootCA.pem" switch/ca/rootCA.pem

cat <<EOF

gen-certs: done.

  $OUT_DIR/rootCA.pem   the CA certificate  (public; also copied to switch/ca/)
  $OUT_DIR/rootCA.key   the CA private key  (SECRET -- back this up, never commit it)
  $OUT_DIR/edge.crt     the edge certificate, $SAN_COUNT hostnames
  $OUT_DIR/edge.key     the edge private key (SECRET)

Next:
  1. Point nginx at edge.crt and edge.key. 'switchnetctl nginx' emits the
     server blocks with the right paths.
  2. On the console, install the Atmosphere disable_ca_verification patch --
     a retail Switch cannot be told to trust a new CA. See switch/README.md.
  3. Check it end to end from a machine on the console's network:

       curl -vk --resolve dauth-lp1.ndas.srv.nintendo.net:443:<YOUR_IP> \\
            https://dauth-lp1.ndas.srv.nintendo.net/keys

     A completed handshake and a JSON key set means DNS, TLS and routing are
     all correct.
EOF
