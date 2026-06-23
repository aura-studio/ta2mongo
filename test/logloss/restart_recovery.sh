#!/usr/bin/env bash
# Empirical test of the B2/B3/B4/B8 "mitigated by restart re-read" claim.
#
# One non-rotating file (size cap >> data, so nothing is ever deleted), so the
# ONLY shutdown loss is the in-flight channel/buffer drop (B2/B3/B4). We:
#   round 1: start the daemon, dump N lines fast (build a backlog), then SIGTERM
#            mid-ingest — the graceful shutdown drops the un-drained buffers.
#   round 2: restart the daemon on the SAME dir; the read offset is in-memory
#            only, so it re-reads the file from the head and should re-ingest the
#            dropped lines (events dedup by #uuid).
# RECOVERED (count == N after restart) ⇒ B2/B3/B4/B8 self-heal ⇒ optional.
# NOT RECOVERED ⇒ they are real loss and must be fixed.
set -u
BIN=/workspace/test/logloss/bin
OUT=/workspace/test/logloss/out
MONGO=${MONGO:-mongodb://mongo:27017}
MODES=${MODES:-"poll event hybrid"}
N=${N:-100000}
RATE=${RATE:-50}    # MB/s: dump fast to build an in-flight backlog
INGEST=${INGEST:-6} # seconds to ingest part of the backlog before SIGTERM
mkdir -p "$OUT"

overall=0
for mode in $MODES; do
  dir=/tmp/rr-$mode; db=rr_$mode; cfg=/tmp/rr-$mode.yaml
  rm -rf "$dir"; mkdir -p "$dir"
  "$BIN/dbctl-linux" -uri "$MONGO" -db "$db" -op drop >/dev/null 2>&1
  cat > "$cfg" <<YAML
role:
  mode: daemon
logging:
  level: warn
dao:
  mongo:
    uri: ${MONGO}/${db}
source:
  tailer:
    logPattern:
      - '${dir}/log.*'
    tailMode: ${mode}
    rescanInterval: 1s
    pollInterval: 100ms
process:
  pipeline:
    batchWorkers: 4
    batchSize: 1000
    flushInterval: 500ms
YAML

  echo "--- [$mode] round 1: daemon + fast dump, SIGTERM mid-ingest ---"
  "$BIN/tango-linux" --config "$cfg" >"$OUT/rr-$mode-1.log" 2>&1 & dpid=$!
  sleep 2
  # size cap 100MB >> N*~230B, so a single log.<ts> file, no rotation/deletion.
  "$BIN/rotwrite-linux" -dir "$dir" -size 100 -keep 5 -lines "$N" -rate "$RATE"
  sleep "$INGEST"
  kill -TERM "$dpid"; wait "$dpid" 2>/dev/null
  after=$("$BIN/dbctl-linux" -uri "$MONGO" -db "$db" -op count 2>/dev/null | grep -oE '[0-9]+$')
  files=$(ls "$dir" 2>/dev/null | wc -l)
  echo "[$mode] after graceful shutdown: ingested=${after:-?} / $N  (files on disk=$files, none deleted)"

  echo "--- [$mode] round 2: restart same dir → expect re-read recovery ---"
  "$BIN/tango-linux" --config "$cfg" >"$OUT/rr-$mode-2.log" 2>&1 & dpid2=$!
  if "$BIN/dbctl-linux" -uri "$MONGO" -db "$db" -coll event -op wait -want "$N" -timeout 120s -stable 15s; then
    verdict="RECOVERED — restart re-read healed the shutdown drop"
  else
    verdict="NOT FULLY RECOVERED — real loss, must fix"; overall=1
  fi
  kill -TERM "$dpid2"; wait "$dpid2" 2>/dev/null
  echo "[$mode] => dropped_at_shutdown=$((N - ${after:-0})) ; $verdict"
  echo
done

[ "$overall" -eq 0 ] \
  && echo "VERDICT: restart re-read recovers shutdown drops → B2/B3/B4/B8 mitigated (optional)" \
  || echo "VERDICT: restart does NOT fully recover → B2/B3/B4 must be fixed"
exit $overall
