#!/usr/bin/env bash
set -euo pipefail

ca_url="https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem"
ca_dir="${TMPDIR:-/tmp}/tango-changestreams"
ca_file="$ca_dir/global-bundle.pem"

usage() {
  cat <<'EOF'
Command changestreams enables or disables Amazon DocumentDB change streams via
the modifyChangeStreams admin command. cfgsync's changestream backend requires
change streams to be enabled on the target database/collection (plain MongoDB
only needs a replica set; DocumentDB needs this command run once).

The script downloads the AWS global-bundle.pem at runtime and rewrites the
Mongo/DocumentDB URI so tlsCAFile points at that downloaded absolute path.

Usage:
  # Enable cluster-wide (all databases/collections) — what the integration
  # tests need, since they use a random throwaway database each run:
  export TANGO_TEST_MONGO_URI='mongodb://user:pass@host:27017/?tls=true&replicaSet=rs0&readPreference=primary&retryWrites=false'
  ./scripts/changestreams.sh -enable

  # Scope to one database (or one collection):
  ./scripts/changestreams.sh -enable -database mydb
  ./scripts/changestreams.sh -enable -database mydb -collection _tango_config

  # Turn it back off (restore original state):
  ./scripts/changestreams.sh -disable

Flags:
  -uri <uri>           Mongo/DocumentDB connection string (default: $TANGO_TEST_MONGO_URI)
  -enable              Enable change streams
  -disable             Disable change streams
  -database <name>     Target database (default: all databases)
  -collection <name>   Target collection (default: all collections)
  -h, --help           Show this help text
EOF
}

download_ca() {
  mkdir -p "$ca_dir"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$ca_url" -o "$ca_file"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$ca_file" "$ca_url"
    return
  fi
  echo "error: curl or wget is required to download $ca_url" >&2
  exit 1
}

rewrite_tls_ca_file() {
  local raw_uri="$1"
  local rewritten

  download_ca

  rewritten="$raw_uri"
  if [[ "$rewritten" == *"tlsCAFile="* ]]; then
    rewritten="$(printf '%s' "$rewritten" | python3 -c 'import os, re, sys; uri = sys.stdin.read(); path = os.environ["TANGO_DOWNLOADED_CA_FILE"]; print(re.sub(r"tlsCAFile=[^&]*", "tlsCAFile=" + path, uri, count=1), end="")')"
  elif [[ "$rewritten" == *"?"* ]]; then
    rewritten+="&tlsCAFile=$ca_file"
  else
    rewritten+="?tlsCAFile=$ca_file"
  fi

  printf '%s' "$rewritten"
}

uri="${TANGO_TEST_MONGO_URI:-}"
enable=false
disable=false
database=""
collection=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -uri)
      [[ $# -ge 2 ]] || { echo "error: missing value for -uri" >&2; usage >&2; exit 2; }
      uri="$2"
      shift 2
      ;;
    -enable)
      enable=true
      shift
      ;;
    -disable)
      disable=true
      shift
      ;;
    -database)
      [[ $# -ge 2 ]] || { echo "error: missing value for -database" >&2; usage >&2; exit 2; }
      database="$2"
      shift 2
      ;;
    -collection)
      [[ $# -ge 2 ]] || { echo "error: missing value for -collection" >&2; usage >&2; exit 2; }
      collection="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$enable" == "$disable" ]]; then
  echo "error: pass exactly one of -enable or -disable" >&2
  usage >&2
  exit 2
fi

if [[ -z "$uri" ]]; then
  echo "error: no connection string (-uri or \$TANGO_TEST_MONGO_URI)" >&2
  exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 not found in PATH" >&2
  exit 1
fi

if ! command -v mongosh >/dev/null 2>&1; then
  echo "error: mongosh not found in PATH" >&2
  exit 1
fi

uri="$(TANGO_DOWNLOADED_CA_FILE="$ca_file" rewrite_tls_ca_file "$uri")"

enable_js=false
action=disabled
if [[ "$enable" == true ]]; then
  enable_js=true
  action=enabled
fi

scope="cluster-wide"
if [[ -n "$database" ]]; then
  scope="$database"
  if [[ -n "$collection" ]]; then
    scope+=".$collection"
  fi
fi

result="$(TANGO_CHANGESTREAM_DATABASE="$database" \
  TANGO_CHANGESTREAM_COLLECTION="$collection" \
  TANGO_CHANGESTREAM_ENABLE="$enable_js" \
  timeout 30s mongosh "$uri" --quiet --eval '
const command = {
  modifyChangeStreams: 1,
  database: process.env.TANGO_CHANGESTREAM_DATABASE || "",
  collection: process.env.TANGO_CHANGESTREAM_COLLECTION || "",
  enable: (process.env.TANGO_CHANGESTREAM_ENABLE || "false") === "true",
};
const res = db.getSiblingDB("admin").runCommand(command);
if (!res || res.ok !== 1) {
  throw new Error("modifyChangeStreams failed: " + JSON.stringify(res));
}
print(JSON.stringify(res));
' 2>&1)" || {
  status=$?
  echo "modifyChangeStreams: $result" >&2
  exit "$status"
}

printf 'change streams %s (%s): %s\n' "$action" "$scope" "$result"
