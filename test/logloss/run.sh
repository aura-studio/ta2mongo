#!/usr/bin/env bash
# Daemon log-loss rotation test harness. For each tail mode (poll/event/hybrid)
# it runs the REAL tango daemon process against a size-rotated "log.<timestamp>"
# file set (max KEEP files, oldest deleted) written at a paced rate, then checks
# that every written line landed in MongoDB (distinct #uuid count == lines).
#
# Runs inside the Ubuntu test container (Linux file/inotify semantics + mongo).
# Pre-built static linux binaries are expected under test/logloss/bin/.
set -u

BIN=/workspace/test/logloss/bin
OUT=/workspace/test/logloss/out
MONGO=${MONGO:-mongodb://mongo:27017}
MODES=${MODES:-"poll event hybrid"}
SIZE_MB=${SIZE_MB:-10}     # per-file cap
KEEP=${KEEP:-5}            # max files kept (oldest deleted beyond this)
LINES=${LINES:-480000}     # total lines (~9-10 files at 10MB → several deletions)
RATE=${RATE:-8}            # write pace MB/s (streaming sim)
RESCAN=${RESCAN:-1s}
LOGLEVEL=${LOGLEVEL:-info}
mkdir -p "$OUT"

echo "=== daemon log-loss rotation test ==="
echo "size=${SIZE_MB}MB keep=${KEEP} lines=${LINES} rate=${RATE}MB/s rescan=${RESCAN}"
echo

overall=0
declare -A RESULT

for mode in $MODES; do
  dir=/tmp/logloss-$mode
  db=logloss_$mode
  cfg=/tmp/tango-$mode.yaml
  dlog=$OUT/daemon-$mode.log
  rm -rf "$dir"; mkdir -p "$dir"

  "$BIN/dbctl-linux" -uri "$MONGO" -db "$db" -op drop >/dev/null 2>&1

  cat > "$cfg" <<YAML
role:
  mode: daemon
logging:
  level: ${LOGLEVEL}
dao:
  mongo:
    uri: ${MONGO}/${db}
source:
  tailer:
    logPattern:
      - '${dir}/log.*'
    tailMode: ${mode}
    rescanInterval: ${RESCAN}
    pollInterval: 100ms
process:
  pipeline:
    batchWorkers: 4
    batchSize: 1000
    flushInterval: 500ms
YAML

  echo "--- [$mode] starting daemon ---"
  "$BIN/tango-linux" --config "$cfg" >"$dlog" 2>&1 &
  dpid=$!
  sleep 2  # let it connect + ensure indexes + initial scan

  if ! kill -0 "$dpid" 2>/dev/null; then
    echo "[$mode] daemon died on startup:"; tail -5 "$dlog"; RESULT[$mode]="DAEMON-DIED"; overall=1; continue
  fi

  echo "--- [$mode] writing rotating logs ---"
  "$BIN/rotwrite-linux" -dir "$dir" -size "$SIZE_MB" -keep "$KEEP" -lines "$LINES" -rate "$RATE"

  echo "--- [$mode] waiting for ingestion to drain ---"
  if "$BIN/dbctl-linux" -uri "$MONGO" -db "$db" -coll event -op wait -want "$LINES" -timeout 240s -stable 15s; then
    RESULT[$mode]="ZERO-LOSS"
  else
    RESULT[$mode]="LOSS"; overall=1
  fi

  # Stop the daemon only AFTER the count is final, so any shutdown-drain drop
  # (a separate, unfixed bug) cannot contaminate the rotation measurement.
  kill -INT "$dpid" 2>/dev/null
  wait "$dpid" 2>/dev/null

  files_now=$(ls "$dir" 2>/dev/null | wc -l)
  echo "[$mode] live files at end=$files_now"
  # Diagnostic: distinguish read-side loss (tailer never saw the lines, e.g.
  # deleted-before-read) from write-side loss (read but BulkWrite dropped the
  # batch, B5). total_lines = lines the tailer READ; event_writes/write_err from
  # the daemon's own cumulative counters.
  echo "[$mode] daemon final counters:"
  grep -E 'cumulative stats|final stats' "$dlog" | tail -1
  grep -oE 'total_lines=[0-9]+|total_event_writes=[0-9]+|total_write_err[a-z]*=[0-9]+|total_dead_letters=[0-9]+|total_parse_err[a-z]*=[0-9]+|total_identity_err[a-z]*=[0-9]+|total_retries=[0-9]+' "$dlog" | tail -7 | tr '\n' ' '; echo
  echo
done

echo "=== SUMMARY ==="
for mode in $MODES; do
  printf "  %-7s %s\n" "$mode" "${RESULT[$mode]:-?}"
done
[ "$overall" -eq 0 ] && echo "VERDICT: ALL ZERO-LOSS" || echo "VERDICT: LOSS DETECTED"
exit $overall
