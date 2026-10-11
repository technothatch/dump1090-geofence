# dump1090-geofence

[![CI](https://github.com/technothatch/dump1090-geofence/actions/workflows/ci.yml/badge.svg)](https://github.com/technothatch/dump1090-geofence/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/technothatch/dump1090-geofence/blob/main/LICENSE)
[![Latest Release](https://img.shields.io/github/v/release/technothatch/dump1090-geofence)](https://github.com/technothatch/dump1090-geofence/releases)
[![Issues](https://img.shields.io/github/issues/technothatch/dump1090-geofence)](https://github.com/technothatch/dump1090-geofence/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/technothatch/dump1090-geofence/pulls)
[![Code size](https://img.shields.io/github/languages/code-size/technothatch/dump1090-geofence)](https://github.com/technothatch/dump1090-geofence)
[![Last commit](https://img.shields.io/github/last-commit/technothatch/dump1090-geofence)](https://github.com/technothatch/dump1090-geofence/commits/main)
[![Go Version](https://img.shields.io/github/go.mod/go-version/technothatch/dump1090-geofence)](https://github.com/technothatch/dump1090-geofence/blob/main/go.mod)
[![Coverage](https://img.shields.io/badge/coverage-80.3%25-brightgreen.svg)](https://github.com/technothatch/dump1090-geofence/actions/workflows/ci.yml)

## What & why

A [dump1090](https://github.com/flightaware/dump1090) / [readsb](https://github.com/wiedehopf/readsb)
decoder sees every aircraft in range — but you should not have to share every
aircraft when you share your feed. `dump1090-geofence` is a small proxy that
sits between your decoder and the people you give the feed to, forwarding only
the aircraft inside a region you configure (a bounding box, a GeoJSON polygon
or a circle), optionally narrowed by altitude band, military-only, on-ground
and ICAO address allow/deny lists. Downstream consumers get the usual Beast or
SBS stream *and* an `aircraft.json` that shows exactly the same filtered view,
so a friend's tar1090 or their own dump1090 instance displays what you agreed
to share — and nothing else.

## Features

- **Region filters**: `--box minLat,minLon,maxLat,maxLon`, a GeoJSON
  `FeatureCollection` file (polygons with holes, multi-polygon unions,
  antimeridian-safe) or `--circle lat,lon,radius_nm` (haversine, same
  semantics as readsb's `?circle=`).
- **Altitude band**: `--above` / `--below` in feet, with `"ground"` handled
  correctly (0 ft when dropped, `alt=ground` in dry-run output).
- **Address policy**: repeatable `--allow-hex` / `--deny-hex`; deny always
  wins, even over allow.
- **Flag filters**: `--military-only` (dbFlags & 1) and `--drop-on-ground`.
- **Fade-out**: aircraft leaving the region stay in the filtered view until
  `--linger` (default 30 s) expires, then disappear quietly.
- **Dual output**: filtered Beast (or SBS, matching the upstream) on
  `--listen`, plus a filtered `GET /data/aircraft.json` and `/healthz` on
  `--http-listen` so a downstream web UI shows the same view.
- **`--dry-run`**: replay an SBS log or an `aircraft.json` document and print
  one `allow/deny` decision per record with the exact reason — no ports bound,
  nothing forwarded. Input is a file path or `-` for stdin.
- **Strict TOML config**: unknown sections or keys are hard errors — a typo'd
  `deny_hex` must never silently mean "deny everybody".
- **Zero dependencies**: standard library only, `go install` is hermetic, small
  enough for a Raspberry Pi.

## Install

Requires Go 1.25 or newer.

```bash
go install github.com/technothatch/dump1090-geofence/cmd/dump1090-geofence@latest
```

Or build from source:

```bash
git clone https://github.com/technothatch/dump1090-geofence.git
cd dump1090-geofence
go build ./...
```

## Quickstart

### 1. Preview decisions with `--dry-run`

Replay three SBS records through a London-ish box with a 30,000 ft ceiling —
no decoder, no ports, input on stdin:

```bash
printf '%s\r\n' \
  'MSG,3,1,1,3C673A,1,2026/10/06,12:00:00.000,2026/10/06,12:00:00.000,BAW123,12000,400,270.5,51.47000,-0.45000,0,7000,0,0,0,0' \
  'MSG,3,1,1,ABCDEF,1,2026/10/06,12:00:01.000,2026/10/06,12:00:01.000,ZZZ999,9000,400,180.0,60.00000,10.00000,0,7000,0,0,0,0' \
  'MSG,3,1,1,4CA901,1,2026/10/06,12:00:02.000,2026/10/06,12:00:02.000,EZY83M,35000,450,95.0,52.10000,1.20000,0,7000,0,0,0,0' \
  | dump1090-geofence --dry-run - --box 50,-5,53,2 --below 30000
```

Output (one line per input record, sorted by hex, then a summary):

```text
hex=3c673a allow=true reason=inside-region alt=12000 pos=51.47000,-0.45000
hex=4ca901 allow=false reason=above-max-alt alt=35000 pos=52.10000,1.20000
hex=abcdef allow=false reason=outside-region alt=9000 pos=60.00000,10.00000
checked=3 allowed=1
```

### 2. Serve the filtered feed

```bash
dump1090-geofence --box 50,-5,53,2
```

```text
dump1090-geofence: region box(50,-5,53,2)
dump1090-geofence: listening on :30006 (beast), http on :8081, upstream beast localhost:30005
dump1090-geofence: polling http://localhost:8080/data/aircraft.json every 1s
```

Point a downstream consumer at the filtered Beast/SBS port (`:30006`) and a
web UI at the filtered JSON. With an empty (or absent) decoder the endpoint
still answers:

```bash
curl http://localhost:8081/data/aircraft.json
```

```json
{"now":1791417613,"messages":0,"aircraft":[]}
```

```bash
curl http://localhost:8081/healthz   # → ok
```

### 3. Share deliberately

```bash
# Circle around London, keep airliners but deny one specific airframe:
dump1090-geofence --circle 51.5,-0.1,40 --above 3000 --deny-hex 4b1d2f

# Same, from a config file:
dump1090-geofence --config fence.toml
```

## Configuration reference

Precedence (lowest to highest): built-in defaults → `DUMP1090_URL`
environment variable → `fence.toml` (`--config`) → command line flags.
Go's flag package accepts both `-flag` and `--flag`. There are no other
environment variables.

| Flag | TOML key | Default | Meaning |
|------|----------|---------|---------|
| `--upstream` | `[upstream] addr` | `localhost:30005` | Decoder feed address (`host:port`) — dump1090/readsb Beast output |
| `--upstream-type` | `[upstream] type` | `beast` | Feed format: `beast` or `sbs` |
| `--listen` | `[output] listen` | `:30006` | Filtered output listen address (format matches upstream type) |
| `--http-listen` | `[output] http_listen` | `:8081` | Listen address for `/data/aircraft.json` and `/healthz`; must differ from `--listen` |
| `--url` | `[decoder] url` | `http://localhost:8080` | Decoder base URL for position polling (env `DUMP1090_URL`) |
| `--poll-interval` | `[decoder] poll_interval` | `1s` | `aircraft.json` poll interval (beast upstream); seconds or duration string |
| `--no-poll` | `[decoder] no_poll` | `false` | Disable decoder polling — only valid with `--upstream-type sbs` |
| `--box` | `[region] box` | — | Region: GeoJSON file path **or** `minLat,minLon,maxLat,maxLon` |
| `--circle` | `[region] circle` | — | Region: `lat,lon,radius_nm` (mutually exclusive with `--box`; exactly one is required) |
| `--above` | `[filters] above_ft` | `-Inf` | Minimum altitude in feet; a band with unknown altitude is denied |
| `--below` | `[filters] below_ft` | `+Inf` | Maximum altitude in feet |
| `--allow-hex` | `[filters] allow_hex` | `[]` | ICAO addresses forwarded regardless of other filters (repeatable / comma-separated) |
| `--deny-hex` | `[filters] deny_hex` | `[]` | ICAO addresses always denied — wins over everything (repeatable) |
| `--military-only` | `[filters] military_only` | `false` | Forward only military aircraft (`dbFlags & 1`) |
| `--drop-on-ground` | `[filters] drop_on_ground` | `false` | Drop aircraft reported on the ground |
| `--linger` | `[filters] linger` | `30s` | Keep an allowed aircraft this long after its last accepted message; `0` disables expiry |
| `--config` | — | — | Path to `fence.toml` (flag only) |
| `--dry-run` | — | off | Replay a log and print decisions instead of serving; value is a file path or `-` for stdin (flag only) |

Hex values are normalized to lowercase; TOML lists are replaced (never
merged) by `--allow-hex` / `--deny-hex` flags.

### Full `fence.toml` example

```toml
# dump1090-geofence configuration. Unknown sections/keys are errors.

[upstream]
type = "beast"          # beast | sbs
addr = "localhost:30005"

[output]
listen = ":30006"       # filtered Beast/SBS output for consumers
http_listen = ":8081"   # /data/aircraft.json + /healthz

[decoder]
url = "http://localhost:8080"
poll_interval = "1s"    # seconds (1) or duration string ("1s")
no_poll = false         # true only with upstream type "sbs"

[region]
box = "50,-5,53,2"      # or a path: box = "polygon.geojson"
# circle = "51.5,-0.1,40"   # lat,lon,radius_nm — mutually exclusive with box

[filters]
above_ft = 500
below_ft = 30000
allow_hex = ["3c673a"]
deny_hex = ["4b1d2f"]
military_only = false
drop_on_ground = true
linger = 30
```

### `polygon.geojson` example

Coordinates are `[longitude, latitude]`, the ring must be closed, and a
`FeatureCollection` of several polygons becomes a union:

```json
{
  "type": "FeatureCollection",
  "features": [
    {
      "type": "Feature",
      "properties": { "name": "home-range" },
      "geometry": {
        "type": "Polygon",
        "coordinates": [
          [
            [-0.60, 51.40],
            [-0.20, 51.40],
            [-0.20, 51.60],
            [-0.60, 51.60],
            [-0.60, 51.40]
          ]
        ]
      }
    }
  ]
}
```

## How it works

**Decision precedence.** Every observation gets exactly one explainable
reason (this is what `--dry-run` prints), in fixed order:

1. `deny-hex` — beats everything, including `allow-hex`, and deletes any held
   state for that airframe immediately.
2. `allow-hex` — bypasses region, altitude and flag filters.
3. `no-position` — a candidate without a position is denied (Beast frames
   carry none; positions come from the polled `aircraft.json`).
4. `outside-region` — point-in-polygon (rings unwrapped across the
   antimeridian) or haversine circle test.
5. `on-ground` — only when `--drop-on-ground` is set.
6. `unknown-altitude` / `below-min-alt` / `above-max-alt` — only when an
   altitude band is configured; partial messages are judged on the merged
   view (held state + new observation).
7. `not-military` — only when `--military-only` is set.
8. `inside-region` — allowed.

**Fade-out / linger.** State is committed only on *allowed* observations; a
denied one leaves the held entry frozen, so data from an aircraft that left
the region can never refresh stale inside-the-fence knowledge. An allowed
aircraft stays in the snapshot and in the forwarding table until `--linger`
(default 30 s) passes with no accepted message, then a once-per-second prune
(and the next snapshot) drop it. `linger = 0` keeps entries until they are
denied by `deny-hex` or the process exits.

**Beast output.** Frames are passed byte-for-byte to every connected client:
heartbeats always, Mode S frames only while the tracker vouches for their
ICAO address. Mode A/C frames (no address) and Radarcape receiver-position
frames are never forwarded. Client command frames are consumed but not
relayed upstream — one consumer must not reconfigure the shared decoder link.
Clients that stall are dropped rather than blocking the bus.

**SBS output.** Each line is parsed into a partial observation, run through
the same verdict, and forwarded verbatim (`\r\n` terminated) only when
allowed; unparseable lines carry no trustworthy address and are skipped.

**Filtered `aircraft.json`.** `GET /data/aircraft.json` on `--http-listen`
serves a real `{"now", "messages", "aircraft"}` document built from the held
state: every aircraft keeps all of its original upstream keys, with
`seen` / `seen_pos` / `messages` recomputed for the filtered view and entries
sorted by hex. The Beast upstream has no positions in-band, so a poll loop
(default every 1 s) reads the decoder's own `aircraft.json` for positions and
`dbFlags`; with an SBS upstream the stream carries positions itself
(`--no-poll` skips the loop entirely).

## Development

```bash
git clone https://github.com/technothatch/dump1090-geofence.git
cd dump1090-geofence

gofmt -l .                 # must print nothing
go vet ./...               # must be clean
go test ./...               # all tests must pass
go build ./...             # must build

# Coverage (CGO needs a C compiler for -covermode atomic):
go test -count=1 -coverprofile coverage.out -covermode atomic ./...
go tool cover -func coverage.out | tail -1
rm -f coverage.out
```

A `Makefile` wraps the same targets — `make build`, `make test`, `make cover`,
`make fmt`, `make vet`, `make race`, `make check` — but `make` is not
required; the commands above are exactly what CI
(`.github/workflows/ci.yml`) runs, plus `golangci-lint` and
`go test -race`. Table-driven tests live next to the code in
`cmd/` and `internal/`.

## Related projects

- [dump1090-fa](https://github.com/flightaware/dump1090-fa) — FlightAware's
  dump1090 fork; Beast/SBS/HTTP outputs this proxy consumes.
- [readsb](https://github.com/wiedehopf/readsb) — high-performance readsb
  decoder with the `?box=` / `?circle=` query API this tool's region flags
  mirror.
- [tar1090](https://github.com/wiedehopf/tar1090) — a fast web UI; point it at
  this proxy's `/data/aircraft.json` to show your filtered view.
- [graphs1090](https://github.com/wiedehopf/graphs1090) — long-term graphs for
  a dump1090/readsb receiver.

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 technothatch.

---

## Related projects

Part of the [technothatch ADS-B toolkit](https://github.com/technothatch/technothatch#readme) - the full index with one-liners for every tool lives there.

- **Run & operate:** [dump1090-stack](https://github.com/technothatch/dump1090-stack) - full Compose stack; [dump1090-healthcheck](https://github.com/technothatch/dump1090-healthcheck) - decoder watchdog; [dump1090-alerts](https://github.com/technothatch/dump1090-alerts) - rule-based alerting
- **Metrics & data:** [dump1090-exporter](https://github.com/technothatch/dump1090-exporter) - Prometheus; [dump1090-influx](https://github.com/technothatch/dump1090-influx) - InfluxDB/Telegraf; [dump1090-grafana](https://github.com/technothatch/dump1090-grafana) - dashboards; [dump1090-recorder](https://github.com/technothatch/dump1090-recorder) - SQLite history; [dump1090-coverage](https://github.com/technothatch/dump1090-coverage) - heatmaps & reports
- **Feeds & networking:** [beast-relay](https://github.com/technothatch/beast-relay) - Beast fan-out; [dump1090-geofence](https://github.com/technothatch/dump1090-geofence) - region-filtered feeds; [dump1090-to-mqtt](https://github.com/technothatch/dump1090-to-mqtt) - MQTT/Home Assistant
- **Interfaces:** [dump1090-dashboard](https://github.com/technothatch/dump1090-dashboard) - web UI; [adsb-tui](https://github.com/technothatch/adsb-tui) - terminal radar; [dump1090-cli](https://github.com/technothatch/dump1090-cli) - aircraft.json CLI; [dump1090-sdk](https://github.com/technothatch/dump1090-sdk) - TypeScript client
- **Testing & decoding:** [dump1090-simulator](https://github.com/technothatch/dump1090-simulator) - synthetic traffic; [dump1090-replay](https://github.com/technothatch/dump1090-replay) - recorded history replay; [squawk-decoder](https://github.com/technothatch/squawk-decoder) - squawk/emitter/ICAO decoder
